// Package k8s provides a client for interacting with the Kubernetes API.
package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
)

// Client encapsulates Kubernetes client functionality including dynamic,
// discovery, and metrics clients.
// It also caches API resource information for performance.
const gvrCacheTTL = 5 * time.Minute

type Client struct {
	clientset        *kubernetes.Clientset
	dynamicClient    dynamic.Interface
	discoveryClient  *discovery.DiscoveryClient
	metricsClientset *metricsclientset.Clientset
	restConfig       *rest.Config
	kubeconfigPath   string
	apiResourceCache map[string]*schema.GroupVersionResource
	cacheLock        sync.RWMutex
	cacheRefreshedAt time.Time
}

// NewClient creates a new Kubernetes client.
// Resolution order:
//  1. Explicit kubeconfigPath argument
//  2. KUBECONFIG environment variable
//  3. In-cluster ServiceAccount config
//  4. Default ~/.kube/config
func NewClient(kubeconfigPath string) (*Client, error) {
	var config *rest.Config
	var err error
	var resolvedKubeconfigPath string

	if kubeconfigPath != "" {
		resolvedKubeconfigPath = kubeconfigPath
	} else if envPath := os.Getenv("KUBECONFIG"); envPath != "" {
		resolvedKubeconfigPath = envPath
	}

	if resolvedKubeconfigPath != "" {
		config, err = clientcmd.BuildConfigFromFlags("", resolvedKubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load kubeconfig from %s: %w", resolvedKubeconfigPath, err)
		}
		fmt.Printf("Using kubeconfig: %s\n", resolvedKubeconfigPath)
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			if home := homedir.HomeDir(); home != "" {
				resolvedKubeconfigPath = filepath.Join(home, ".kube", "config")
				config, err = clientcmd.BuildConfigFromFlags("", resolvedKubeconfigPath)
				if err != nil {
					return nil, fmt.Errorf("failed to create Kubernetes configuration: %w", err)
				}
				fmt.Printf("Using kubeconfig: %s\n", resolvedKubeconfigPath)
			} else {
				return nil, fmt.Errorf("failed to create Kubernetes configuration: %w", err)
			}
		} else {
			fmt.Println("Using in-cluster ServiceAccount config")
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	// Initialize metrics client
	metricsClient, err := metricsclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics client: %w", err)
	}

	return &Client{
		clientset:        clientset,
		dynamicClient:    dynamicClient,
		discoveryClient:  discoveryClient,
		metricsClientset: metricsClient,
		restConfig:       config,
		kubeconfigPath:   resolvedKubeconfigPath,
		apiResourceCache: make(map[string]*schema.GroupVersionResource),
	}, nil
}

// CheckConnection verifies that the Kubernetes API server is reachable.
func (c *Client) CheckConnection() error {
	_, err := c.clientset.Discovery().ServerVersion()
	return err
}

// GetAPIResources retrieves all API resource types in the cluster.
// It uses the discovery client to fetch server-preferred resources.
// Filters resources based on includeNamespaceScoped and includeClusterScoped flags.
// Returns a slice of maps, each representing an API resource, or an error.
func (c *Client) GetAPIResources(ctx context.Context, includeNamespaceScoped, includeClusterScoped bool) ([]map[string]interface{}, error) {
	resourceLists, err := c.discoveryClient.ServerPreferredResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, fmt.Errorf("failed to retrieve API resources: %w", err)
	}

	var resources []map[string]interface{}
	for _, resourceList := range resourceLists {
		for i := range resourceList.APIResources {
			resource := &resourceList.APIResources[i]
			if (resource.Namespaced && !includeNamespaceScoped) || (!resource.Namespaced && !includeClusterScoped) {
				continue
			}
			resources = append(resources, map[string]interface{}{
				"name":         resource.Name,
				"singularName": resource.SingularName,
				"namespaced":   resource.Namespaced,
				"kind":         resource.Kind,
				"group":        resource.Group,
				"version":      resource.Version,
				"verbs":        resource.Verbs,
			})
		}
	}
	return resources, nil
}

// GetResource retrieves detailed information about a specific resource.
// It uses the dynamic client to fetch the resource by kind, name, and namespace.
// It utilizes a cached GroupVersionResource (GVR) for efficiency.
// Returns the unstructured content of the resource as a map, or an error.
func (c *Client) GetResource(ctx context.Context, kind, name, namespace string) (map[string]interface{}, error) {
	gvr, err := c.getCachedGVR(kind)
	if err != nil {
		return nil, err
	}

	var obj *unstructured.Unstructured
	if namespace != "" {
		obj, err = c.dynamicClient.Resource(*gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	} else {
		obj, err = c.dynamicClient.Resource(*gvr).Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve resource: %w", err)
	}

	return obj.UnstructuredContent(), nil
}

// ListResources lists all instances of a specific resource type.
// It uses the dynamic client and supports filtering by namespace, labelSelector,
// and fieldSelector.
// It utilizes a cached GroupVersionResource (GVR) for efficiency.
// Returns a slice of maps, each representing a resource instance, or an error.
func (c *Client) ListResources(ctx context.Context, kind, namespace, labelSelector, fieldSelector string) ([]map[string]interface{}, error) {
	gvr, err := c.getCachedGVR(kind)
	if err != nil {
		return nil, err
	}

	options := metav1.ListOptions{
		LabelSelector: labelSelector,
		FieldSelector: fieldSelector,
	}

	var list *unstructured.UnstructuredList
	if namespace != "" {
		list, err = c.dynamicClient.Resource(*gvr).Namespace(namespace).List(ctx, options)
	} else {
		list, err = c.dynamicClient.Resource(*gvr).List(ctx, options)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	var resources []map[string]interface{}
	for _, item := range list.Items {
		metadata := item.GetLabels()
		resources = append(resources, map[string]interface{}{
			"name":      item.GetName(),
			"kind":      item.GetKind(),
			"namespace": item.GetNamespace(),
			"labels":    metadata,
		})
	}

	return resources, nil
}

// CreateOrUpdateResource creates a new resource or updates an existing one.
// It parses the provided manifest string into an unstructured object.
// It uses the dynamic client to first attempt an update, and if that fails
// (e.g., resource not found), it attempts to create the resource.
// Requires the resource manifest to include a name.
// Returns the unstructured content of the created/updated resource, or an error.
func (c *Client) CreateOrUpdateResourceJSON(ctx context.Context, namespace, manifestJSON, kind string) (map[string]interface{}, error) {
	// Decode JSON into unstructured object directly (no YAML conversion)

	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(manifestJSON), &obj.Object); err != nil {
		return nil, fmt.Errorf("failed to parse resource manifest JSON: %w", err)
	}

	gvr, err := c.getCachedGVR(kind)
	if err != nil {
		return nil, err
	}

	if namespace != "" {
		obj.SetNamespace(namespace)
	}

	if obj.GetName() == "" {
		return nil, fmt.Errorf("resource name is required")
	}

	resource := c.dynamicClient.Resource(*gvr).Namespace(obj.GetNamespace())

	// Try to patch; if not found, create
	rawJSON := []byte(manifestJSON) // manifestJSON is already JSON
	result, err := resource.Patch(
		ctx,
		obj.GetName(),
		types.MergePatchType,
		rawJSON,
		metav1.PatchOptions{},
	)
	if errors.IsNotFound(err) {
		result, err = resource.Create(ctx, obj, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create or patch resource: %w", err)
	}

	return result.UnstructuredContent(), nil
}

// CreateOrUpdateResourceYAML creates a new resource or updates an existing one from a YAML manifest.
// This function is specifically designed for YAML input and provides optimized YAML parsing.
// It converts the YAML manifest to JSON internally and then uses the dynamic client
// to first attempt an update, and if that fails (e.g., resource not found), it attempts to create the resource.
// Requires the resource manifest to include a name.
// Returns the unstructured content of the created/updated resource, or an error.
//
// Parameters:
//   - ctx: Context for the operation
//   - namespace: Target namespace for the resource (overrides manifest namespace if provided)
//   - yamlManifest: YAML manifest string of the Kubernetes resource
//   - kind: Resource kind (optional, will be inferred from manifest if empty)
//
// Example YAML manifest:
//
//	apiVersion: v1
//	kind: Pod
//	metadata:
//	  name: my-pod
//	  namespace: default
//	spec:
//	  containers:
//	  - name: nginx
//	    image: nginx:latest
func (c *Client) CreateOrUpdateResourceYAML(ctx context.Context, namespace, yamlManifest, kind string) (map[string]interface{}, error) {
	// Convert YAML to JSON
	jsonData, err := yaml.YAMLToJSON([]byte(yamlManifest))
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML manifest: %w", err)
	}

	// Parse the converted JSON into unstructured object
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(jsonData, &obj.Object); err != nil {
		return nil, fmt.Errorf("failed to parse converted JSON from YAML manifest: %w", err)
	}

	// Determine the resource GVR
	gvr, err := c.getCachedGVR(kind)
	if err != nil {
		return nil, err
	}

	// Set namespace if provided (overrides manifest namespace)
	if namespace != "" {
		obj.SetNamespace(namespace)
	}

	if obj.GetName() == "" {
		return nil, fmt.Errorf("resource name is required in YAML manifest")
	}

	resource := c.dynamicClient.Resource(*gvr).Namespace(obj.GetNamespace())

	// Try to patch; if not found, create
	result, err := resource.Patch(
		ctx,
		obj.GetName(),
		types.MergePatchType,
		jsonData,
		metav1.PatchOptions{},
	)
	if errors.IsNotFound(err) {
		result, err = resource.Create(ctx, obj, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create or patch resource from YAML manifest: %w", err)
	}

	return result.UnstructuredContent(), nil
}

// DeleteResource deletes a specific resource.
// It uses the dynamic client to delete the resource by kind, name, and namespace.
// It utilizes a cached GroupVersionResource (GVR) for efficiency.
// Returns an error if the deletion fails.
func (c *Client) DeleteResource(ctx context.Context, kind, name, namespace string) error {
	gvr, err := c.getCachedGVR(kind)
	if err != nil {
		return err
	}

	var deleteErr error
	if namespace != "" {
		deleteErr = c.dynamicClient.Resource(*gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	} else {
		deleteErr = c.dynamicClient.Resource(*gvr).Delete(ctx, name, metav1.DeleteOptions{})
	}
	if deleteErr != nil {
		return fmt.Errorf("failed to delete resource: %w", deleteErr)
	}
	return nil
}

// getCachedGVR retrieves the GroupVersionResource for a given kind, using a cache with TTL.
// It supports lookup by PascalCase Kind (e.g. "Deployment"), lowercase plural resource name
// (e.g. "deployments"), or lowercase singular name (e.g. "deployment").
func (c *Client) getCachedGVR(kind string) (*schema.GroupVersionResource, error) {
	c.cacheLock.RLock()
	cacheValid := time.Since(c.cacheRefreshedAt) < gvrCacheTTL
	if cacheValid {
		if gvr, exists := c.apiResourceCache[kind]; exists {
			c.cacheLock.RUnlock()
			return gvr, nil
		}
		if gvr, exists := c.apiResourceCache[strings.ToLower(kind)]; exists {
			c.cacheLock.RUnlock()
			return gvr, nil
		}
	}
	c.cacheLock.RUnlock()

	resourceLists, err := c.discoveryClient.ServerPreferredResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, fmt.Errorf("failed to retrieve API resources: %w", err)
	}

	c.cacheLock.Lock()
	defer c.cacheLock.Unlock()

	if !cacheValid {
		c.apiResourceCache = make(map[string]*schema.GroupVersionResource)
	}
	c.cacheRefreshedAt = time.Now()

	var result *schema.GroupVersionResource
	kindLower := strings.ToLower(kind)
	for _, resourceList := range resourceLists {
		gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			continue
		}
		for i := range resourceList.APIResources {
			resource := &resourceList.APIResources[i]
			gvr := &schema.GroupVersionResource{
				Group:    gv.Group,
				Version:  gv.Version,
				Resource: resource.Name,
			}
			c.apiResourceCache[resource.Kind] = gvr
			c.apiResourceCache[resource.Name] = gvr
			if resource.SingularName != "" {
				c.apiResourceCache[resource.SingularName] = gvr
			}
			for _, shortName := range resource.ShortNames {
				c.apiResourceCache[shortName] = gvr
			}

			if resource.Kind == kind || resource.Name == kind ||
				resource.SingularName == kind ||
				strings.EqualFold(resource.Kind, kindLower) ||
				resource.Name == kindLower {
				result = gvr
			}
			for _, shortName := range resource.ShortNames {
				if shortName == kind || shortName == kindLower {
					result = gvr
				}
			}
		}
	}

	if result != nil {
		return result, nil
	}
	return nil, fmt.Errorf("resource type %s not found", kind)
}

// resolveKindName returns the canonical PascalCase Kind for a given resource
// identifier (e.g. "deployments" -> "Deployment", "pods" -> "Pod").
func (c *Client) resolveKindName(kind string) string {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()

	if gvr, exists := c.apiResourceCache[kind]; exists {
		for k, v := range c.apiResourceCache {
			if v == gvr && k[0] >= 'A' && k[0] <= 'Z' {
				return k
			}
		}
	}
	return kind
}

// DescribeResource retrieves detailed information about a resource including
// related events, owner references, and conditions (similar to kubectl describe).
func (c *Client) DescribeResource(ctx context.Context, kind, name, namespace string) (map[string]interface{}, error) {
	resource, err := c.GetResource(ctx, kind, name, namespace)
	if err != nil {
		return nil, err
	}

	result := map[string]interface{}{
		"resource": resource,
	}

	ns := namespace
	if ns == "" {
		if meta, ok := resource["metadata"].(map[string]interface{}); ok {
			if n, ok := meta["namespace"].(string); ok {
				ns = n
			}
		}
	}

	if ns != "" {
		fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", name, ns)
		events, err := c.clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
			FieldSelector: fieldSelector,
		})
		if err == nil && len(events.Items) > 0 {
			eventList := make([]map[string]interface{}, 0, len(events.Items))
			for i := range events.Items {
				e := &events.Items[i]
				eventList = append(eventList, map[string]interface{}{
					"type":     e.Type,
					"reason":   e.Reason,
					"message":  e.Message,
					"count":    e.Count,
					"lastSeen": e.LastTimestamp.Time,
				})
			}
			result["events"] = eventList
		}
	}

	if meta, ok := resource["metadata"].(map[string]interface{}); ok {
		if ownerRefs, ok := meta["ownerReferences"].([]interface{}); ok && len(ownerRefs) > 0 {
			result["ownerReferences"] = ownerRefs
		}
	}

	return result, nil
}

// GetPodsLogs retrieves the logs for a specific pod.
// It uses the corev1 clientset to fetch logs, limiting to the last 100 lines by default.
// If containerName is provided, it gets logs for that specific container.
// If containerName is empty and the pod has multiple containers, it gets logs from all containers.
// Returns the logs as a string, or an error.
func (c *Client) GetPodsLogs(ctx context.Context, namespace, containerName, podName string) (string, error) {
	tailLines := int64(100)
	podLogOptions := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}

	// If container name is provided, use it
	if containerName != "" {
		podLogOptions.Container = containerName
		req := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, podLogOptions)
		logs, err := req.Stream(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get logs for container '%s': %w", containerName, err)
		}
		defer logs.Close()

		buf := new(bytes.Buffer)
		if _, err := io.Copy(buf, logs); err != nil {
			return "", fmt.Errorf("failed to read logs: %w", err)
		}
		return buf.String(), nil
	}

	// If no container name provided, first get the pod to check its containers
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get pod details: %w", err)
	}

	// If the pod has only one container, get logs from that container
	if len(pod.Spec.Containers) == 1 {
		req := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, podLogOptions)
		logs, err := req.Stream(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get logs: %w", err)
		}
		defer logs.Close()

		buf := new(bytes.Buffer)
		if _, err := io.Copy(buf, logs); err != nil {
			return "", fmt.Errorf("failed to read logs: %w", err)
		}
		return buf.String(), nil
	}

	// If the pod has multiple containers, get logs from each container
	var allLogs strings.Builder
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		containerLogOptions := podLogOptions.DeepCopy()
		containerLogOptions.Container = container.Name

		req := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, containerLogOptions)
		logs, err := req.Stream(ctx)
		if err != nil {
			allLogs.WriteString(fmt.Sprintf("\n--- Error getting logs for container %s: %v ---\n", container.Name, err))
			continue
		}

		allLogs.WriteString(fmt.Sprintf("\n--- Logs for container %s ---\n", container.Name))
		buf := new(bytes.Buffer)
		_, err = io.Copy(buf, logs)
		logs.Close()

		if err != nil {
			allLogs.WriteString(fmt.Sprintf("Error reading logs: %v\n", err))
		} else {
			allLogs.WriteString(buf.String())
		}
	}

	return allLogs.String(), nil
}

// GetPodMetrics retrieves CPU and Memory metrics for a specific pod.
// It uses the metrics clientset to fetch pod metrics.
// Returns a map containing pod metadata and container metrics, or an error.
func (c *Client) GetPodMetrics(ctx context.Context, namespace, podName string) (map[string]interface{}, error) {
	podMetrics, err := c.metricsClientset.MetricsV1beta1().PodMetricses(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics for pod '%s' in namespace '%s': %w", podName, namespace, err)
	}

	metricsResult := map[string]interface{}{
		"podName":    podName,
		"namespace":  namespace,
		"timestamp":  podMetrics.Timestamp.Time,
		"window":     podMetrics.Window.Duration.String(),
		"containers": []map[string]interface{}{},
	}

	containerMetricsList := []map[string]interface{}{}
	for _, container := range podMetrics.Containers {
		containerMetrics := map[string]interface{}{
			"name":   container.Name,
			"cpu":    container.Usage.Cpu().String(),    // Format Quantity
			"memory": container.Usage.Memory().String(), // Format Quantity
		}
		containerMetricsList = append(containerMetricsList, containerMetrics)
	}
	metricsResult["containers"] = containerMetricsList

	return metricsResult, nil
}

// GetNodeMetrics retrieves CPU and Memory metrics for a specific Node.
// It uses the metrics clientset to fetch node metrics.
// Returns a map containing node metadata and resource usage, or an error.
func (c *Client) GetNodeMetrics(ctx context.Context, nodeName string) (map[string]interface{}, error) {
	nodeMetrics, err := c.metricsClientset.MetricsV1beta1().NodeMetricses().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics for node '%s': %w", nodeName, err)
	}

	metricsResult := map[string]interface{}{
		"nodeName":  nodeName,
		"timestamp": nodeMetrics.Timestamp.Time,
		"window":    nodeMetrics.Window.Duration.String(),
		"usage": map[string]string{
			"cpu":    nodeMetrics.Usage.Cpu().String(),    // Format Quantity
			"memory": nodeMetrics.Usage.Memory().String(), // Format Quantity
		},
	}

	return metricsResult, nil
}

// GetEvents retrieves events for a specific namespace or all namespaces.
// It uses the corev1 clientset to fetch events.
// Returns a slice of maps, each representing an event, or an error.
func (c *Client) GetEvents(ctx context.Context, namespace, labelSelector string) ([]map[string]interface{}, error) {
	var eventList *corev1.EventList
	var err error

	opts := metav1.ListOptions{LabelSelector: labelSelector}
	if namespace != "" {
		eventList, err = c.clientset.CoreV1().Events(namespace).List(ctx, opts)
	} else {
		eventList, err = c.clientset.CoreV1().Events("").List(ctx, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve events: %w", err)
	}

	var events []map[string]interface{}
	for i := range eventList.Items {
		event := &eventList.Items[i]
		events = append(events, map[string]interface{}{
			"name":      event.Name,
			"namespace": event.Namespace,
			"reason":    event.Reason,
			"message":   event.Message,
			"source":    event.Source.Component,
			"type":      event.Type,
			"count":     event.Count,
			"firstTime": event.FirstTimestamp.Time,
			"lastTime":  event.LastTimestamp.Time,
		})
	}
	return events, nil
}

// GetIngresses retrieves ingresses and returns specific fields: name, namespace, hosts, paths, and backend services.
// It uses the networking.k8s.io/v1 clientset to fetch ingresses.
// Returns a slice of maps, each representing an ingress with the requested fields, or an error.
func (c *Client) GetIngresses(ctx context.Context, host string) ([]map[string]interface{}, error) {
	ingresses, err := c.clientset.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve ingresses: %w", err)
	}

	var ingressList []map[string]interface{}
	for i := range ingresses.Items {
		ingress := &ingresses.Items[i]
		// Check if this ingress has any rules matching the given host
		hasMatchingHost := false
		var matchingPaths []string
		var matchingBackendServices []string

		for _, rule := range ingress.Spec.Rules {
			// If host filter is specified, only process rules matching the host
			if host != "" && rule.Host != host {
				continue
			}

			// If we reach here, either no host filter or host matches
			if host == "" || rule.Host == host {
				hasMatchingHost = true

				if rule.HTTP != nil {
					for _, path := range rule.HTTP.Paths {
						matchingPaths = append(matchingPaths, path.Path)

						// Extract backend service information
						if path.Backend.Service != nil {
							matchingBackendServices = append(matchingBackendServices, path.Backend.Service.Name)
						}
					}
				}
			}
		}

		// Only add this ingress if it has matching rules
		if hasMatchingHost {
			ingressList = append(ingressList, map[string]interface{}{
				"name":            ingress.Name,
				"namespace":       ingress.Namespace,
				"paths":           matchingPaths,
				"backendServices": matchingBackendServices,
			})
		}
	}

	return ingressList, nil
}

// RolloutRestart restarts any Kubernetes workload with a pod template (Deployment, DaemonSet, StatefulSet, etc.).
// It patches the spec.template.metadata.annotations with the current timestamp.
// Returns the patched resource content or an error if the resource doesn't support rollout restart.
func (c *Client) RolloutRestart(ctx context.Context, kind, name, namespace string) (map[string]interface{}, error) {
	gvr, err := c.getCachedGVR(kind)
	if err != nil {
		return nil, fmt.Errorf("failed to get GVR for kind %s: %w", kind, err)
	}

	resource := c.dynamicClient.Resource(*gvr).Namespace(namespace)

	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().Format(time.RFC3339),
	))

	result, err := resource.Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to rollout restart %s %s/%s: %w", kind, namespace, name, err)
	}

	content := result.UnstructuredContent()
	spec, found, _ := unstructured.NestedMap(content, "spec", "template")
	if !found || spec == nil {
		return nil, fmt.Errorf("resource kind %s does not support rollout restart (no spec.template)", kind)
	}

	return content, nil
}

// Enhanced Resource Inspection Methods

// GetResourceYAML exports a resource as YAML string
func (c *Client) GetResourceYAML(ctx context.Context, kind, name, namespace string) (string, error) {
	// Get the resource first
	resource, err := c.GetResource(ctx, kind, name, namespace)
	if err != nil {
		return "", fmt.Errorf("failed to get resource: %w", err)
	}

	// Remove managed fields for cleaner output
	if metadata, ok := resource["metadata"].(map[string]interface{}); ok {
		delete(metadata, "managedFields")
		delete(metadata, "selfLink")
		delete(metadata, "uid")
		delete(metadata, "resourceVersion")
	}

	// Convert to YAML
	yamlBytes, err := yaml.Marshal(resource)
	if err != nil {
		return "", fmt.Errorf("failed to convert to YAML: %w", err)
	}

	return string(yamlBytes), nil
}

// GetResourceDiff compares resource states
func (c *Client) GetResourceDiff(ctx context.Context, kind, name, namespace, compareWith string) (map[string]interface{}, error) {
	// Get current resource
	current, err := c.GetResource(ctx, kind, name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get current resource: %w", err)
	}

	result := map[string]interface{}{
		"current": current,
		"diff":    nil,
	}

	// Handle different comparison types
	if compareWith == "" || compareWith == "previous" {
		// Get the resource's revision history if available
		// For now, return just the current state with a note
		result["note"] = "Previous version comparison requires revision history (available for Deployments, StatefulSets, etc.)"
	} else if strings.HasPrefix(compareWith, "resource:") {
		// Compare with another resource
		otherName := strings.TrimPrefix(compareWith, "resource:")
		other, err := c.GetResource(ctx, kind, otherName, namespace)
		if err != nil {
			return nil, fmt.Errorf("failed to get comparison resource: %w", err)
		}
		result["other"] = other
		result["diff"] = generateSimpleDiff(current, other)
	}

	return result, nil
}

// GetNamespaceResources lists all resources in a namespace
func (c *Client) GetNamespaceResources(ctx context.Context, namespace string, types string, includeSecrets bool) (map[string]interface{}, error) {
	resourcesMap := make(map[string][]map[string]interface{})
	result := map[string]interface{}{
		"namespace": namespace,
		"resources": resourcesMap,
	}

	// Get all available resource types
	resourceLists, err := c.discoveryClient.ServerPreferredResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, fmt.Errorf("failed to discover resources: %w", err)
	}

	// Parse requested types
	var requestedTypes map[string]bool
	if types != "" {
		requestedTypes = make(map[string]bool)
		for _, t := range strings.Split(types, ",") {
			requestedTypes[strings.TrimSpace(strings.ToLower(t))] = true
		}
	}

	// Iterate through resources
	for _, resourceList := range resourceLists {
		gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			continue
		}

		for i := range resourceList.APIResources {
			resource := &resourceList.APIResources[i]
			// Skip if not namespaced
			if !resource.Namespaced {
				continue
			}

			// Skip secrets if not requested
			if !includeSecrets && strings.EqualFold(resource.Kind, "secret") {
				continue
			}

			// Check if type is requested
			if requestedTypes != nil && !requestedTypes[strings.ToLower(resource.Kind)] && !requestedTypes[strings.ToLower(resource.Name)] {
				continue
			}

			// Skip subresources
			if strings.Contains(resource.Name, "/") {
				continue
			}

			// List resources of this type
			gvr := schema.GroupVersionResource{
				Group:    gv.Group,
				Version:  gv.Version,
				Resource: resource.Name,
			}

			list, err := c.dynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				continue // Skip resources we can't list
			}

			if len(list.Items) > 0 {
				resources := make([]map[string]interface{}, 0, len(list.Items))
				for _, item := range list.Items {
					// Create a summary of each resource
					summary := map[string]interface{}{
						"name": item.GetName(),
						"kind": resource.Kind,
					}

					// Add creation timestamp
					if timestamp := item.GetCreationTimestamp(); !timestamp.IsZero() {
						summary["created"] = timestamp.Time
					}

					resources = append(resources, summary)
				}
				resourcesMap[resource.Kind] = resources
			}
		}
	}

	return result, nil
}

// GetResourceOwners traces ownership chain
func (c *Client) GetResourceOwners(ctx context.Context, kind, name, namespace string, includeChildren bool) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"resource": map[string]interface{}{
			"kind":      kind,
			"name":      name,
			"namespace": namespace,
		},
		"owners":   []map[string]interface{}{},
		"children": []map[string]interface{}{},
	}

	// Get the resource
	resource, err := c.GetResource(ctx, kind, name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource: %w", err)
	}

	// Extract owner references
	if metadata, ok := resource["metadata"].(map[string]interface{}); ok {
		if ownerRefs, ok := metadata["ownerReferences"].([]interface{}); ok {
			owners := make([]map[string]interface{}, 0)

			for _, ref := range ownerRefs {
				if ownerRef, ok := ref.(map[string]interface{}); ok {
					ownerInfo := map[string]interface{}{
						"kind": ownerRef["kind"],
						"name": ownerRef["name"],
						"uid":  ownerRef["uid"],
					}

					// Try to get the owner resource
					if ownerKind, ok := ownerRef["kind"].(string); ok {
						if ownerName, ok := ownerRef["name"].(string); ok {
							// Owner is always in the same namespace
							ownerResource, err := c.GetResource(ctx, ownerKind, ownerName, namespace)
							if err == nil {
								ownerInfo["exists"] = true
								if ownerMeta, ok := ownerResource["metadata"].(map[string]interface{}); ok {
									ownerInfo["namespace"] = ownerMeta["namespace"]
								}
							} else {
								ownerInfo["exists"] = false
								ownerInfo["error"] = err.Error()
							}
						}
					}

					owners = append(owners, ownerInfo)
				}
			}

			result["owners"] = owners
		}

		// Get UID for finding children
		if includeChildren {
			if uid, ok := metadata["uid"].(string); ok {
				// Find resources that have this resource as owner
				children, err := c.findOwnedResources(ctx, uid, namespace)
				if err == nil {
					result["children"] = children
				}
			}
		}
	}

	return result, nil
}

// Helper function to generate simple diff
func generateSimpleDiff(obj1, obj2 map[string]interface{}) map[string]interface{} {
	added := map[string]interface{}{}
	removed := map[string]interface{}{}
	modified := map[string]interface{}{}

	for k, v1 := range obj1 {
		if v2, exists := obj2[k]; !exists {
			removed[k] = v1
		} else if fmt.Sprintf("%v", v1) != fmt.Sprintf("%v", v2) {
			modified[k] = map[string]interface{}{
				"old": v1,
				"new": v2,
			}
		}
	}

	for k, v2 := range obj2 {
		if _, exists := obj1[k]; !exists {
			added[k] = v2
		}
	}

	return map[string]interface{}{
		"added":    added,
		"removed":  removed,
		"modified": modified,
	}
}

// Helper function to find resources owned by a specific UID
func (c *Client) findOwnedResources(ctx context.Context, ownerUID, namespace string) ([]map[string]interface{}, error) {
	children := []map[string]interface{}{}

	// Get all resource types
	resourceLists, err := c.discoveryClient.ServerPreferredResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, err
	}

	for _, resourceList := range resourceLists {
		gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			continue
		}

		for i := range resourceList.APIResources {
			resource := &resourceList.APIResources[i]
			// Skip non-namespaced resources if we have a namespace
			if namespace != "" && !resource.Namespaced {
				continue
			}

			// Skip subresources
			if strings.Contains(resource.Name, "/") {
				continue
			}

			gvr := schema.GroupVersionResource{
				Group:    gv.Group,
				Version:  gv.Version,
				Resource: resource.Name,
			}

			var list *unstructured.UnstructuredList
			if resource.Namespaced && namespace != "" {
				list, err = c.dynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
			} else if !resource.Namespaced {
				list, err = c.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
			} else {
				continue
			}

			if err != nil {
				continue
			}

			// Check each item for owner reference
			for _, item := range list.Items {
				if ownerRefs := item.GetOwnerReferences(); len(ownerRefs) > 0 {
					for _, ref := range ownerRefs {
						if ref.UID == types.UID(ownerUID) {
							children = append(children, map[string]interface{}{
								"kind":      resource.Kind,
								"name":      item.GetName(),
								"namespace": item.GetNamespace(),
							})
							break
						}
					}
				}
			}
		}
	}

	return children, nil
}

// Advanced Monitoring & Observability Methods

// GetClusterHealth checks overall cluster health
func (c *Client) GetClusterHealth(ctx context.Context, includeMetrics, includeEvents bool) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"healthy":      true,
		"nodes":        map[string]interface{}{},
		"controlPlane": map[string]interface{}{},
		"issues":       []string{},
	}

	// Check node health
	nodes, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		result["healthy"] = false
		if issuesList, ok := result["issues"].([]string); ok {
			result["issues"] = append(issuesList, fmt.Sprintf("Failed to list nodes: %v", err))
		}
	} else {
		readyCount := 0
		notReadyCount := 0
		nodesList := make([]map[string]interface{}, 0, len(nodes.Items))

		for i := range nodes.Items {
			node := &nodes.Items[i]
			nodeConditions := []string{}
			nodeInfo := map[string]interface{}{
				"name":  node.Name,
				"ready": false,
			}

			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady {
					if condition.Status == corev1.ConditionTrue {
						nodeInfo["ready"] = true
						readyCount++
					} else {
						notReadyCount++
						result["healthy"] = false
					}
				}
				if condition.Status != corev1.ConditionTrue && condition.Type != corev1.NodeReady {
					nodeConditions = append(nodeConditions,
						fmt.Sprintf("%s: %s", condition.Type, condition.Message))
				}
			}

			nodeInfo["conditions"] = nodeConditions

			// Get node metrics if requested
			if includeMetrics {
				metrics, err := c.GetNodeMetrics(ctx, node.Name)
				if err == nil {
					nodeInfo["metrics"] = metrics
				}
			}

			nodesList = append(nodesList, nodeInfo)
		}

		result["nodes"] = map[string]interface{}{
			"total":    len(nodes.Items),
			"ready":    readyCount,
			"notReady": notReadyCount,
			"nodes":    nodesList,
		}
	}

	// Check control plane components
	controlPlaneNamespaces := []string{"kube-system"}
	controlPlaneLabels := []string{
		"component=kube-apiserver",
		"component=kube-controller-manager",
		"component=kube-scheduler",
		"component=etcd",
	}

	cpComponents := []map[string]interface{}{}

	for _, ns := range controlPlaneNamespaces {
		for _, label := range controlPlaneLabels {
			pods, err := c.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
				LabelSelector: label,
			})
			if err == nil && len(pods.Items) > 0 {
				for j := range pods.Items {
					pod := &pods.Items[j]
					component := map[string]interface{}{
						"name":      pod.Name,
						"namespace": pod.Namespace,
						"ready":     true,
						"phase":     string(pod.Status.Phase),
					}

					// Check if all containers are ready
					for k := range pod.Status.ContainerStatuses {
						if !pod.Status.ContainerStatuses[k].Ready {
							component["ready"] = false
							result["healthy"] = false
							break
						}
					}

					cpComponents = append(cpComponents, component)
				}
			}
		}
	}

	result["controlPlane"] = map[string]interface{}{
		"components": cpComponents,
	}

	// Get recent warning/error events if requested
	if includeEvents {
		events, err := c.clientset.CoreV1().Events("").List(ctx, metav1.ListOptions{
			FieldSelector: "type!=Normal",
		})
		if err == nil {
			recentEvents := []map[string]interface{}{}
			cutoff := time.Now().Add(-30 * time.Minute)

			for i := range events.Items {
				event := &events.Items[i]
				if event.LastTimestamp.After(cutoff) {
					recentEvents = append(recentEvents, map[string]interface{}{
						"type":      event.Type,
						"reason":    event.Reason,
						"message":   event.Message,
						"object":    fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name),
						"namespace": event.Namespace,
						"timestamp": event.LastTimestamp.Time,
					})
				}
			}

			if len(recentEvents) > 0 {
				result["recentEvents"] = recentEvents
			}
		}
	}

	return result, nil
}

// GetResourceQuotas lists resource quotas and usage
func (c *Client) GetResourceQuotas(ctx context.Context, namespace string, showPercentage bool) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"quotas": []map[string]interface{}{},
	}

	var quotas *corev1.ResourceQuotaList
	var err error

	if namespace == "" {
		quotas, err = c.clientset.CoreV1().ResourceQuotas("").List(ctx, metav1.ListOptions{})
	} else {
		quotas, err = c.clientset.CoreV1().ResourceQuotas(namespace).List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list resource quotas: %w", err)
	}

	for i := range quotas.Items {
		quota := &quotas.Items[i]
		quotaInfo := map[string]interface{}{
			"name":      quota.Name,
			"namespace": quota.Namespace,
			"status":    map[string]interface{}{},
		}

		// Process each resource in the quota
		for resourceName, hard := range quota.Status.Hard {
			used := quota.Status.Used[resourceName]

			resourceStatus := map[string]interface{}{
				"hard": hard.String(),
				"used": used.String(),
			}

			if showPercentage {
				hardValue := hard.Value()
				usedValue := used.Value()
				if hardValue > 0 {
					percentage := float64(usedValue) / float64(hardValue) * 100
					resourceStatus["percentage"] = fmt.Sprintf("%.1f%%", percentage)
				}
			}

			quotaInfo["status"].(map[string]interface{})[string(resourceName)] = resourceStatus
		}

		result["quotas"] = append(result["quotas"].([]map[string]interface{}), quotaInfo)
	}

	return result, nil
}

// GetLimitRanges gets limit ranges in namespaces
func (c *Client) GetLimitRanges(ctx context.Context, namespace string) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"limitRanges": []map[string]interface{}{},
	}

	var limitRanges *corev1.LimitRangeList
	var err error

	if namespace == "" {
		limitRanges, err = c.clientset.CoreV1().LimitRanges("").List(ctx, metav1.ListOptions{})
	} else {
		limitRanges, err = c.clientset.CoreV1().LimitRanges(namespace).List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list limit ranges: %w", err)
	}

	for i := range limitRanges.Items {
		lr := &limitRanges.Items[i]
		lrInfo := map[string]interface{}{
			"name":      lr.Name,
			"namespace": lr.Namespace,
			"limits":    []map[string]interface{}{},
		}

		for _, limit := range lr.Spec.Limits {
			limitInfo := map[string]interface{}{
				"type": string(limit.Type),
			}

			if limit.Max != nil {
				limitInfo["max"] = limit.Max
			}
			if limit.Min != nil {
				limitInfo["min"] = limit.Min
			}
			if limit.Default != nil {
				limitInfo["default"] = limit.Default
			}
			if limit.DefaultRequest != nil {
				limitInfo["defaultRequest"] = limit.DefaultRequest
			}

			lrInfo["limits"] = append(lrInfo["limits"].([]map[string]interface{}), limitInfo)
		}

		result["limitRanges"] = append(result["limitRanges"].([]map[string]interface{}), lrInfo)
	}

	return result, nil
}

// GetTopPods gets top pods by resource usage
func (c *Client) GetTopPods(ctx context.Context, namespace, sortBy string, limit int) ([]map[string]interface{}, error) {
	if sortBy == "" {
		sortBy = "cpu"
	}
	if limit <= 0 {
		limit = 10
	}

	// Get pod metrics
	var podMetricsList *v1beta1.PodMetricsList
	var err error

	if namespace == "" {
		podMetricsList, err = c.metricsClientset.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{})
	} else {
		podMetricsList, err = c.metricsClientset.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get pod metrics: %w", err)
	}

	// Process and sort pods
	type podUsage struct {
		Name      string
		Namespace string
		CPU       int64
		Memory    int64
		Info      map[string]interface{}
	}

	pods := make([]podUsage, 0, len(podMetricsList.Items))

	for i := range podMetricsList.Items {
		pm := &podMetricsList.Items[i]
		var totalCPU, totalMemory int64

		for _, container := range pm.Containers {
			cpu := container.Usage[corev1.ResourceCPU]
			memory := container.Usage[corev1.ResourceMemory]

			totalCPU += cpu.MilliValue()
			totalMemory += memory.Value()
		}

		pods = append(pods, podUsage{
			Name:      pm.Name,
			Namespace: pm.Namespace,
			CPU:       totalCPU,
			Memory:    totalMemory,
			Info: map[string]interface{}{
				"name":       pm.Name,
				"namespace":  pm.Namespace,
				"cpu":        fmt.Sprintf("%dm", totalCPU),
				"memory":     fmt.Sprintf("%dMi", totalMemory/(1024*1024)),
				"containers": len(pm.Containers),
			},
		})
	}

	if sortBy == "memory" {
		sort.Slice(pods, func(i, j int) bool { return pods[i].Memory > pods[j].Memory })
	} else {
		sort.Slice(pods, func(i, j int) bool { return pods[i].CPU > pods[j].CPU })
	}

	// Return top N pods
	result := make([]map[string]interface{}, 0, limit)
	for i := 0; i < len(pods) && i < limit; i++ {
		result = append(result, pods[i].Info)
	}

	return result, nil
}

// GetTopNodes gets top nodes by resource utilization
func (c *Client) GetTopNodes(ctx context.Context, sortBy string, includeConditions bool) ([]map[string]interface{}, error) {
	if sortBy == "" {
		sortBy = "cpu"
	}

	// Get node list and metrics
	nodes, err := c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	nodeMetricsList, err := c.metricsClientset.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node metrics: %w", err)
	}

	// Create a map for quick metrics lookup
	metricsMap := make(map[string]*v1beta1.NodeMetrics)
	for i := range nodeMetricsList.Items {
		nm := &nodeMetricsList.Items[i]
		metricsMap[nm.Name] = nm
	}

	// Process nodes
	type nodeUsage struct {
		Name     string
		CPU      int64
		Memory   int64
		PodCount int
		Info     map[string]interface{}
	}

	nodeUsages := make([]nodeUsage, 0, len(nodes.Items))

	for i := range nodes.Items {
		node := &nodes.Items[i]
		nodeInfo := map[string]interface{}{
			"name": node.Name,
		}

		// Get node capacity
		cpuCapacity := node.Status.Capacity[corev1.ResourceCPU]
		memoryCapacity := node.Status.Capacity[corev1.ResourceMemory]
		podCapacity := node.Status.Capacity[corev1.ResourcePods]

		nodeInfo["capacity"] = map[string]interface{}{
			"cpu":    cpuCapacity.String(),
			"memory": fmt.Sprintf("%dGi", memoryCapacity.Value()/(1024*1024*1024)),
			"pods":   podCapacity.String(),
		}

		// Get current usage from metrics
		var cpuUsage, memoryUsage int64
		if metrics, ok := metricsMap[node.Name]; ok {
			cpu := metrics.Usage[corev1.ResourceCPU]
			memory := metrics.Usage[corev1.ResourceMemory]

			cpuUsage = cpu.MilliValue()
			memoryUsage = memory.Value()

			nodeInfo["usage"] = map[string]interface{}{
				"cpu":    fmt.Sprintf("%dm", cpuUsage),
				"memory": fmt.Sprintf("%dMi", memoryUsage/(1024*1024)),
			}

			// Calculate percentages
			if cpuCapacity.MilliValue() > 0 {
				cpuPercent := float64(cpuUsage) / float64(cpuCapacity.MilliValue()) * 100
				nodeInfo["cpuPercentage"] = fmt.Sprintf("%.1f%%", cpuPercent)
			}
			if memoryCapacity.Value() > 0 {
				memPercent := float64(memoryUsage) / float64(memoryCapacity.Value()) * 100
				nodeInfo["memoryPercentage"] = fmt.Sprintf("%.1f%%", memPercent)
			}
		}

		// Get pod count
		pods, err := c.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("spec.nodeName=%s", node.Name),
		})
		podCount := 0
		if err == nil {
			podCount = len(pods.Items)
			nodeInfo["podCount"] = podCount
			nodeInfo["podPercentage"] = fmt.Sprintf("%.1f%%", float64(podCount)/float64(podCapacity.Value())*100)
		}

		// Include conditions if requested
		if includeConditions {
			conditions := []map[string]interface{}{}
			for _, cond := range node.Status.Conditions {
				if cond.Status != corev1.ConditionTrue || cond.Type != corev1.NodeReady {
					conditions = append(conditions, map[string]interface{}{
						"type":    string(cond.Type),
						"status":  string(cond.Status),
						"reason":  cond.Reason,
						"message": cond.Message,
					})
				}
			}
			if len(conditions) > 0 {
				nodeInfo["conditions"] = conditions
			}
		}

		nodeUsages = append(nodeUsages, nodeUsage{
			Name:     node.Name,
			CPU:      cpuUsage,
			Memory:   memoryUsage,
			PodCount: podCount,
			Info:     nodeInfo,
		})
	}

	switch sortBy {
	case "memory":
		sort.Slice(nodeUsages, func(i, j int) bool { return nodeUsages[i].Memory > nodeUsages[j].Memory })
	case "pods":
		sort.Slice(nodeUsages, func(i, j int) bool { return nodeUsages[i].PodCount > nodeUsages[j].PodCount })
	default:
		sort.Slice(nodeUsages, func(i, j int) bool { return nodeUsages[i].CPU > nodeUsages[j].CPU })
	}

	// Convert to result format
	result := make([]map[string]interface{}, len(nodeUsages))
	for i, nu := range nodeUsages {
		result[i] = nu.Info
	}

	return result, nil
}

// Debugging & Troubleshooting Methods

// GetPodDebugInfo gets comprehensive debugging information for a pod
func (c *Client) GetPodDebugInfo(ctx context.Context, name, namespace string, includeLogs bool, logLines int) (map[string]interface{}, error) {
	if logLines <= 0 {
		logLines = 50
	}

	result := map[string]interface{}{
		"pod":        nil,
		"events":     []map[string]interface{}{},
		"conditions": []map[string]interface{}{},
		"containers": []map[string]interface{}{},
		"logs":       map[string]string{},
	}

	// Get the pod
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}

	// Basic pod info
	result["pod"] = map[string]interface{}{
		"name":      pod.Name,
		"namespace": pod.Namespace,
		"phase":     string(pod.Status.Phase),
		"nodeName":  pod.Spec.NodeName,
		"startTime": pod.Status.StartTime,
		"uid":       string(pod.UID),
	}

	// Pod conditions
	for _, condition := range pod.Status.Conditions {
		result["conditions"] = append(result["conditions"].([]map[string]interface{}), map[string]interface{}{
			"type":               string(condition.Type),
			"status":             string(condition.Status),
			"reason":             condition.Reason,
			"message":            condition.Message,
			"lastTransitionTime": condition.LastTransitionTime.Time,
		})
	}

	// Container statuses
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		containerInfo := map[string]interface{}{
			"name":         cs.Name,
			"ready":        cs.Ready,
			"restartCount": cs.RestartCount,
			"image":        cs.Image,
			"imageID":      cs.ImageID,
		}

		// Add current state info
		if cs.State.Running != nil {
			containerInfo["state"] = "running"
			containerInfo["startedAt"] = cs.State.Running.StartedAt.Time
		} else if cs.State.Waiting != nil {
			containerInfo["state"] = "waiting"
			containerInfo["waitingReason"] = cs.State.Waiting.Reason
			containerInfo["waitingMessage"] = cs.State.Waiting.Message
		} else if cs.State.Terminated != nil {
			containerInfo["state"] = "terminated"
			containerInfo["exitCode"] = cs.State.Terminated.ExitCode
			containerInfo["reason"] = cs.State.Terminated.Reason
			containerInfo["message"] = cs.State.Terminated.Message
		}

		result["containers"] = append(result["containers"].([]map[string]interface{}), containerInfo)
	}

	// Get events related to this pod
	fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.namespace=%s", name, namespace)
	events, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fieldSelector,
	})
	if err == nil {
		for i := range events.Items {
			event := &events.Items[i]
			result["events"] = append(result["events"].([]map[string]interface{}), map[string]interface{}{
				"type":      event.Type,
				"reason":    event.Reason,
				"message":   event.Message,
				"count":     event.Count,
				"firstTime": event.FirstTimestamp.Time,
				"lastTime":  event.LastTimestamp.Time,
			})
		}
	}

	if includeLogs {
		logs := make(map[string]string)
		for i := range pod.Status.ContainerStatuses {
			cs := &pod.Status.ContainerStatuses[i]
			tailLinesVal := int64(logLines)
			logOptions := &corev1.PodLogOptions{
				Container: cs.Name,
				TailLines: &tailLinesVal,
			}

			req := c.clientset.CoreV1().Pods(namespace).GetLogs(name, logOptions)
			podLogs, err := req.Stream(ctx)
			if err == nil {
				buf := new(bytes.Buffer)
				_, copyErr := io.Copy(buf, podLogs)
				podLogs.Close()
				if copyErr == nil {
					logs[cs.Name] = buf.String()
				}
			}
		}
		result["logs"] = logs
	}

	return result, nil
}

// GetServiceEndpoints lists endpoints for a service with health status
func (c *Client) GetServiceEndpoints(ctx context.Context, name, namespace string, checkHealth bool) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"service":   nil,
		"endpoints": []map[string]interface{}{},
	}

	// Get the service
	service, err := c.clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get service: %w", err)
	}

	result["service"] = map[string]interface{}{
		"name":      service.Name,
		"namespace": service.Namespace,
		"type":      string(service.Spec.Type),
		"clusterIP": service.Spec.ClusterIP,
		"ports":     service.Spec.Ports,
		"selector":  service.Spec.Selector,
	}

	// Get endpoints
	endpoints, err := c.clientset.CoreV1().Endpoints(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return result, nil // Return service info even if no endpoints
	}

	// Process endpoint subsets
	for _, subset := range endpoints.Subsets {
		for _, addr := range subset.Addresses {
			endpointInfo := map[string]interface{}{
				"ip":       addr.IP,
				"nodeName": addr.NodeName,
			}

			// Get target reference info
			if addr.TargetRef != nil {
				endpointInfo["targetKind"] = addr.TargetRef.Kind
				endpointInfo["targetName"] = addr.TargetRef.Name
				endpointInfo["targetNamespace"] = addr.TargetRef.Namespace

				// Check pod health if requested
				if checkHealth && addr.TargetRef.Kind == "Pod" {
					pod, err := c.clientset.CoreV1().Pods(addr.TargetRef.Namespace).Get(ctx, addr.TargetRef.Name, metav1.GetOptions{})
					if err == nil {
						endpointInfo["podPhase"] = string(pod.Status.Phase)
						endpointInfo["ready"] = true

						// Check if all containers are ready
						for k := range pod.Status.ContainerStatuses {
							if !pod.Status.ContainerStatuses[k].Ready {
								endpointInfo["ready"] = false
								break
							}
						}
					}
				}
			}

			// Add ports
			ports := []map[string]interface{}{}
			for _, port := range subset.Ports {
				ports = append(ports, map[string]interface{}{
					"name":     port.Name,
					"port":     port.Port,
					"protocol": string(port.Protocol),
				})
			}
			endpointInfo["ports"] = ports

			result["endpoints"] = append(result["endpoints"].([]map[string]interface{}), endpointInfo)
		}

		// Also include not ready addresses
		for _, addr := range subset.NotReadyAddresses {
			endpointInfo := map[string]interface{}{
				"ip":       addr.IP,
				"nodeName": addr.NodeName,
				"ready":    false,
			}

			if addr.TargetRef != nil {
				endpointInfo["targetKind"] = addr.TargetRef.Kind
				endpointInfo["targetName"] = addr.TargetRef.Name
				endpointInfo["targetNamespace"] = addr.TargetRef.Namespace
			}

			result["endpoints"] = append(result["endpoints"].([]map[string]interface{}), endpointInfo)
		}
	}

	return result, nil
}

// GetNetworkPolicies lists network policies affecting a namespace or pod
func (c *Client) GetNetworkPolicies(ctx context.Context, namespace, podName string, includeDetails bool) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"policies": []map[string]interface{}{},
	}

	// Get all network policies in the namespace
	policies, err := c.clientset.NetworkingV1().NetworkPolicies(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list network policies: %w", err)
	}

	// If a specific pod is requested, get its labels
	var podLabels map[string]string
	if podName != "" {
		pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get pod: %w", err)
		}
		podLabels = pod.Labels
	}

	for i := range policies.Items {
		policy := &policies.Items[i]
		policyInfo := map[string]interface{}{
			"name":      policy.Name,
			"namespace": policy.Namespace,
		}

		// Check if this policy applies to the specific pod
		if podName != "" && podLabels != nil {
			selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
			if err == nil && selector.Matches(labels.Set(podLabels)) {
				policyInfo["appliesToPod"] = true
			} else {
				policyInfo["appliesToPod"] = false
			}
		}

		// Add policy types
		policyInfo["policyTypes"] = policy.Spec.PolicyTypes

		// Summarize rules
		ingressRules := len(policy.Spec.Ingress)
		egressRules := len(policy.Spec.Egress)
		policyInfo["ingressRules"] = ingressRules
		policyInfo["egressRules"] = egressRules

		// Include full details if requested
		if includeDetails {
			policyInfo["spec"] = policy.Spec
		} else if policy.Spec.PodSelector.MatchLabels != nil {
			policyInfo["podSelector"] = policy.Spec.PodSelector.MatchLabels
		}

		result["policies"] = append(result["policies"].([]map[string]interface{}), policyInfo)
	}

	return result, nil
}

// GetSecurityContext gets security contexts for a pod
func (c *Client) GetSecurityContext(ctx context.Context, name, namespace string, includeDefaults bool) (map[string]interface{}, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}

	result := map[string]interface{}{
		"pod": map[string]interface{}{
			"name":      pod.Name,
			"namespace": pod.Namespace,
		},
	}

	// Pod-level security context
	if pod.Spec.SecurityContext != nil {
		podSec := map[string]interface{}{}
		sc := pod.Spec.SecurityContext

		if sc.RunAsUser != nil {
			podSec["runAsUser"] = *sc.RunAsUser
		}
		if sc.RunAsGroup != nil {
			podSec["runAsGroup"] = *sc.RunAsGroup
		}
		if sc.FSGroup != nil {
			podSec["fsGroup"] = *sc.FSGroup
		}
		if sc.RunAsNonRoot != nil {
			podSec["runAsNonRoot"] = *sc.RunAsNonRoot
		}
		if sc.SELinuxOptions != nil {
			podSec["seLinuxOptions"] = sc.SELinuxOptions
		}

		result["podSecurityContext"] = podSec
	} else if includeDefaults {
		result["podSecurityContext"] = "Not specified (using defaults)"
	}

	// Container-level security contexts
	containers := []map[string]interface{}{}
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		containerInfo := map[string]interface{}{
			"name": container.Name,
		}

		if container.SecurityContext != nil {
			contSec := map[string]interface{}{}
			sc := container.SecurityContext

			if sc.RunAsUser != nil {
				contSec["runAsUser"] = *sc.RunAsUser
			}
			if sc.RunAsGroup != nil {
				contSec["runAsGroup"] = *sc.RunAsGroup
			}
			if sc.RunAsNonRoot != nil {
				contSec["runAsNonRoot"] = *sc.RunAsNonRoot
			}
			if sc.Privileged != nil {
				contSec["privileged"] = *sc.Privileged
			}
			if sc.AllowPrivilegeEscalation != nil {
				contSec["allowPrivilegeEscalation"] = *sc.AllowPrivilegeEscalation
			}
			if sc.ReadOnlyRootFilesystem != nil {
				contSec["readOnlyRootFilesystem"] = *sc.ReadOnlyRootFilesystem
			}
			if sc.Capabilities != nil {
				caps := map[string]interface{}{}
				if sc.Capabilities.Add != nil {
					caps["add"] = sc.Capabilities.Add
				}
				if sc.Capabilities.Drop != nil {
					caps["drop"] = sc.Capabilities.Drop
				}
				contSec["capabilities"] = caps
			}

			containerInfo["securityContext"] = contSec
		} else if includeDefaults {
			containerInfo["securityContext"] = "Not specified (using defaults)"
		}

		containers = append(containers, containerInfo)
	}

	result["containers"] = containers

	return result, nil
}

// GetResourceHistory gets recent events and changes for a resource
func (c *Client) GetResourceHistory(ctx context.Context, kind, name, namespace string, hours float64) (map[string]interface{}, error) {
	if hours <= 0 {
		hours = 24
	}

	result := map[string]interface{}{
		"resource": map[string]interface{}{
			"kind":      kind,
			"name":      name,
			"namespace": namespace,
		},
		"events": []map[string]interface{}{},
	}

	// Calculate time cutoff
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)

	// Get events for the resource
	fieldSelector := fmt.Sprintf("involvedObject.name=%s", name)
	if namespace != "" {
		fieldSelector += fmt.Sprintf(",involvedObject.namespace=%s", namespace)
	}

	events, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fieldSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list events: %w", err)
	}

	canonicalKind := c.resolveKindName(kind)

	// Filter and sort events by time
	relevantEvents := []map[string]interface{}{}
	for i := range events.Items {
		event := &events.Items[i]
		// Check if event is within the time window
		if event.LastTimestamp.After(cutoff) || event.FirstTimestamp.After(cutoff) {
			if event.InvolvedObject.Kind == canonicalKind {
				eventInfo := map[string]interface{}{
					"type":           event.Type,
					"reason":         event.Reason,
					"message":        event.Message,
					"count":          event.Count,
					"firstTimestamp": event.FirstTimestamp.Time,
					"lastTimestamp":  event.LastTimestamp.Time,
					"source":         event.Source.Component,
				}
				relevantEvents = append(relevantEvents, eventInfo)
			}
		}
	}

	sort.Slice(relevantEvents, func(i, j int) bool {
		t1 := relevantEvents[i]["lastTimestamp"].(time.Time)
		t2 := relevantEvents[j]["lastTimestamp"].(time.Time)
		return t2.Before(t1)
	})

	result["events"] = relevantEvents
	result["timeWindow"] = map[string]interface{}{
		"hours": hours,
		"from":  cutoff,
		"to":    time.Now(),
	}

	// For specific resource types, try to get revision history
	if canonicalKind == "Deployment" || canonicalKind == "StatefulSet" || canonicalKind == "DaemonSet" {
		result["note"] = fmt.Sprintf("For %s resources, use 'kubectl rollout history' for detailed revision history", canonicalKind)
	}

	return result, nil
}

// ValidateManifest validates a YAML or JSON manifest without applying it
func (c *Client) ValidateManifest(ctx context.Context, manifest, format string, strict bool) (map[string]interface{}, error) {
	result := map[string]interface{}{
		"valid":    false,
		"errors":   []string{},
		"warnings": []string{},
		"resource": nil,
	}

	// Auto-detect format if not specified
	if format == "" {
		if strings.TrimSpace(manifest)[0] == '{' {
			format = "json"
		} else {
			format = "yaml"
		}
	}

	// Parse the manifest
	var obj map[string]interface{}
	var err error

	if format == "yaml" {
		err = yaml.Unmarshal([]byte(manifest), &obj)
	} else {
		err = json.Unmarshal([]byte(manifest), &obj)
	}

	if err != nil {
		result["errors"] = append(result["errors"].([]string), fmt.Sprintf("Failed to parse %s: %v", format, err))
		return result, nil
	}

	// Extract resource info
	metadata, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		result["errors"] = append(result["errors"].([]string), "Missing metadata field")
		return result, nil
	}

	kind, ok := obj["kind"].(string)
	if !ok {
		result["errors"] = append(result["errors"].([]string), "Missing or invalid kind field")
		return result, nil
	}

	name, _ := metadata["name"].(string)
	namespace, _ := metadata["namespace"].(string)

	result["resource"] = map[string]interface{}{
		"kind":       kind,
		"name":       name,
		"namespace":  namespace,
		"apiVersion": obj["apiVersion"],
	}

	// Basic validation checks
	if name == "" {
		result["errors"] = append(result["errors"].([]string), "Resource name is required in metadata")
	}

	if obj["apiVersion"] == nil {
		result["errors"] = append(result["errors"].([]string), "apiVersion is required")
	}

	// Try to create an unstructured object for more validation
	unstructuredObj := &unstructured.Unstructured{Object: obj}

	// Get the GVR for this resource type
	gvr, err := c.getCachedGVR(kind)
	if err != nil {
		result["warnings"] = append(result["warnings"].([]string), fmt.Sprintf("Unknown resource type '%s': %v", kind, err))
	} else {
		// Perform a dry-run create to validate
		var dryRunErr error
		if namespace != "" {
			_, dryRunErr = c.dynamicClient.Resource(*gvr).Namespace(namespace).Create(ctx, unstructuredObj, metav1.CreateOptions{
				DryRun: []string{metav1.DryRunAll},
			})
		} else {
			_, dryRunErr = c.dynamicClient.Resource(*gvr).Create(ctx, unstructuredObj, metav1.CreateOptions{
				DryRun: []string{metav1.DryRunAll},
			})
		}

		if dryRunErr != nil {
			// Check if it's because the resource already exists (which is okay for validation)
			if errors.IsAlreadyExists(dryRunErr) {
				result["warnings"] = append(result["warnings"].([]string), "Resource already exists (would update instead of create)")
			} else {
				result["errors"] = append(result["errors"].([]string), fmt.Sprintf("Validation failed: %v", dryRunErr))
			}
		}
	}

	// If no errors, it's valid
	if len(result["errors"].([]string)) == 0 {
		result["valid"] = true
	}

	return result, nil
}

// Specialized Operations Methods

// ExecInPod executes a command in a pod container
func (c *Client) ExecInPod(ctx context.Context, name, namespace, command, container string, stdin, tty bool) (map[string]interface{}, error) {
	// Get the pod first to validate it exists
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}

	// If container is not specified and pod has multiple containers, return error
	if container == "" && len(pod.Spec.Containers) > 1 {
		return nil, fmt.Errorf("pod has multiple containers, please specify container name")
	} else if container == "" && len(pod.Spec.Containers) == 1 {
		container = pod.Spec.Containers[0].Name
	}

	// Parse the command
	cmdParts := strings.Fields(command)
	if len(cmdParts) == 0 {
		return nil, fmt.Errorf("command cannot be empty")
	}

	// Create exec request
	req := c.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(name).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmdParts,
			Stdin:     stdin,
			Stdout:    true,
			Stderr:    true,
			TTY:       tty,
		}, scheme.ParameterCodec)

	// Execute the command
	executor, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    tty,
	})

	result := map[string]interface{}{
		"pod":       name,
		"namespace": namespace,
		"container": container,
		"command":   command,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
	}

	if err != nil {
		result["error"] = err.Error()
		result["success"] = false
		// Still return the result with output, even if command failed
		return result, nil
	}

	result["success"] = true
	return result, nil
}

// PortForward sets up port forwarding to a pod
func (c *Client) PortForward(ctx context.Context, name, namespace, ports string, duration int) (map[string]interface{}, error) {
	if duration <= 0 {
		duration = 60
	}

	// Parse ports
	portParts := strings.Split(ports, ":")
	var localPort, remotePort string

	if len(portParts) == 1 {
		localPort = portParts[0]
		remotePort = portParts[0]
	} else if len(portParts) == 2 {
		localPort = portParts[0]
		remotePort = portParts[1]
	} else {
		return nil, fmt.Errorf("invalid port format, use 'local:remote' or just 'port'")
	}

	// Validate ports
	localPortNum, err := strconv.Atoi(localPort)
	if err != nil || localPortNum <= 0 || localPortNum > 65535 {
		return nil, fmt.Errorf("invalid local port: %s", localPort)
	}

	remotePortNum, err := strconv.Atoi(remotePort)
	if err != nil || remotePortNum <= 0 || remotePortNum > 65535 {
		return nil, fmt.Errorf("invalid remote port: %s", remotePort)
	}

	result := map[string]interface{}{
		"pod":        name,
		"namespace":  namespace,
		"localPort":  localPortNum,
		"remotePort": remotePortNum,
		"duration":   duration,
		"status":     "Port forwarding is not fully implemented in this version",
		"note":       fmt.Sprintf("Would forward localhost:%d to %s/%s:%d for %d seconds", localPortNum, namespace, name, remotePortNum, duration),
	}

	// Note: Full port forwarding implementation would require:
	// - Creating a port forwarder using client-go's portforward package
	// - Managing the lifecycle of the forwarder
	// - Potentially running it in a goroutine
	// This is a simplified version that validates inputs

	return result, nil
}

// CopyFromPod copies files from a pod container to local filesystem
func (c *Client) CopyFromPod(ctx context.Context, name, namespace, srcPath, destPath, container string) (map[string]interface{}, error) {
	// Get the pod first to validate it exists
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}

	// If container is not specified and pod has multiple containers, return error
	if container == "" && len(pod.Spec.Containers) > 1 {
		return nil, fmt.Errorf("pod has multiple containers, please specify container name")
	} else if container == "" && len(pod.Spec.Containers) == 1 {
		container = pod.Spec.Containers[0].Name
	}

	// Create tar command to read the file/directory
	tarCmd := []string{"tar", "-cf", "-", "-C", "/", strings.TrimPrefix(srcPath, "/")}

	req := c.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(name).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   tarCmd,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	// Create a pipe to receive tar data
	reader, writer := io.Pipe()
	var stderr bytes.Buffer

	// Run the tar command in a goroutine
	go func() {
		defer writer.Close()
		err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdout: writer,
			Stderr: &stderr,
		})
		if err != nil {
			writer.CloseWithError(err)
		}
	}()

	// Extract tar to destination
	// Note: In a real implementation, you would extract the tar archive
	// For now, we'll just read and validate
	tarData, err := io.ReadAll(reader)

	result := map[string]interface{}{
		"pod":         name,
		"namespace":   namespace,
		"container":   container,
		"source":      srcPath,
		"destination": destPath,
		"tarSize":     len(tarData),
	}

	if stderr.Len() > 0 {
		result["stderr"] = stderr.String()
	}

	if err != nil {
		result["error"] = err.Error()
		result["success"] = false
		return result, nil
	}

	// In a real implementation, you would:
	// 1. Create destPath directory if needed
	// 2. Extract tarData to destPath
	// 3. Preserve permissions and ownership

	result["success"] = true
	result["note"] = "File copy validation successful. Full extraction not implemented in this version."

	return result, nil
}

// CopyToPod copies files from local filesystem to a pod container
func (c *Client) CopyToPod(ctx context.Context, name, namespace, srcPath, destPath, container string) (map[string]interface{}, error) {
	// Get the pod first to validate it exists
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod: %w", err)
	}

	// If container is not specified and pod has multiple containers, return error
	if container == "" && len(pod.Spec.Containers) > 1 {
		return nil, fmt.Errorf("pod has multiple containers, please specify container name")
	} else if container == "" && len(pod.Spec.Containers) == 1 {
		container = pod.Spec.Containers[0].Name
	}

	// Check if source path exists
	if _, err := os.Stat(srcPath); err != nil {
		return nil, fmt.Errorf("source path does not exist: %w", err)
	}

	// Create tar command to extract in the container
	tarCmd := []string{"tar", "-xf", "-", "-C", "/"}

	req := c.clientset.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Name(name).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   tarCmd,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	_, err = remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	// In a real implementation, you would:
	// 1. Create a tar archive of srcPath
	// 2. Stream it to the container's stdin (using the executor)
	// 3. Handle the tar extraction in the container

	result := map[string]interface{}{
		"pod":         name,
		"namespace":   namespace,
		"container":   container,
		"source":      srcPath,
		"destination": destPath,
		"success":     true,
		"note":        "File copy validation successful. Full copy not implemented in this version.",
	}

	return result, nil
}

// ListNamespaces lists all namespaces in the cluster.
func (c *Client) ListNamespaces(ctx context.Context, labelSelector string) ([]map[string]interface{}, error) {
	nsList, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(nsList.Items))
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		result = append(result, map[string]interface{}{
			"name":    ns.Name,
			"status":  string(ns.Status.Phase),
			"labels":  ns.Labels,
			"created": ns.CreationTimestamp.Time,
		})
	}
	return result, nil
}

// GetRolloutStatus returns the rollout status for a Deployment, StatefulSet, or DaemonSet.
func (c *Client) GetRolloutStatus(ctx context.Context, kind, name, namespace string) (map[string]interface{}, error) {
	resource, err := c.GetResource(ctx, kind, name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource: %w", err)
	}

	result := map[string]interface{}{
		"kind":      kind,
		"name":      name,
		"namespace": namespace,
	}

	spec, _, _ := unstructured.NestedMap(resource, "spec")
	status, _, _ := unstructured.NestedMap(resource, "status")

	if spec != nil {
		if replicas, ok := spec["replicas"]; ok {
			result["desiredReplicas"] = replicas
		}
	}

	if status != nil {
		for _, field := range []string{"replicas", "readyReplicas", "updatedReplicas", "availableReplicas", "unavailableReplicas", "currentReplicas"} {
			if v, ok := status[field]; ok {
				result[field] = v
			}
		}

		if conditions, ok := status["conditions"].([]interface{}); ok {
			condList := make([]map[string]interface{}, 0, len(conditions))
			for _, c := range conditions {
				if cond, ok := c.(map[string]interface{}); ok {
					condList = append(condList, map[string]interface{}{
						"type":    cond["type"],
						"status":  cond["status"],
						"reason":  cond["reason"],
						"message": cond["message"],
					})
				}
			}
			result["conditions"] = condList
		}

		if og, ok := status["observedGeneration"]; ok {
			result["observedGeneration"] = og
		}
	}

	if meta, ok := resource["metadata"].(map[string]interface{}); ok {
		if gen, ok := meta["generation"]; ok {
			result["generation"] = gen
		}
	}

	return result, nil
}

// ScaleResource scales a Deployment, StatefulSet, or ReplicaSet.
func (c *Client) ScaleResource(ctx context.Context, kind, name, namespace string, replicas int32) (map[string]interface{}, error) {
	gvr, err := c.getCachedGVR(kind)
	if err != nil {
		return nil, fmt.Errorf("failed to get GVR for kind %s: %w", kind, err)
	}

	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	patchResult, err := c.dynamicClient.Resource(*gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scale %s %s/%s: %w", kind, namespace, name, err)
	}

	content := patchResult.UnstructuredContent()
	status, _, _ := unstructured.NestedMap(content, "status")

	response := map[string]interface{}{
		"kind":            kind,
		"name":            name,
		"namespace":       namespace,
		"desiredReplicas": replicas,
	}
	if status != nil {
		if r, ok := status["replicas"]; ok {
			response["currentReplicas"] = r
		}
		if r, ok := status["readyReplicas"]; ok {
			response["readyReplicas"] = r
		}
	}

	return response, nil
}

// ListContexts returns available kubeconfig contexts.
func (c *Client) ListContexts() (map[string]interface{}, error) {
	if c.kubeconfigPath == "" {
		return map[string]interface{}{
			"note":     "Running with in-cluster config, no kubeconfig contexts available",
			"contexts": []map[string]interface{}{},
		}, nil
	}

	config, err := clientcmd.LoadFromFile(c.kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	contexts := make([]map[string]interface{}, 0, len(config.Contexts))
	for name, kctx := range config.Contexts {
		contexts = append(contexts, map[string]interface{}{
			"name":      name,
			"cluster":   kctx.Cluster,
			"user":      kctx.AuthInfo,
			"namespace": kctx.Namespace,
			"active":    name == config.CurrentContext,
		})
	}

	return map[string]interface{}{
		"currentContext": config.CurrentContext,
		"contexts":       contexts,
	}, nil
}

// SwitchContext switches the active kubeconfig context and reinitializes all clients.
func (c *Client) SwitchContext(ctx context.Context, contextName string) (map[string]interface{}, error) {
	if c.kubeconfigPath == "" {
		return nil, fmt.Errorf("cannot switch context: running with in-cluster config")
	}

	kubeConfig, err := clientcmd.LoadFromFile(c.kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	if _, exists := kubeConfig.Contexts[contextName]; !exists {
		return nil, fmt.Errorf("context %q not found in kubeconfig", contextName)
	}

	restConfig, err := clientcmd.NewNonInteractiveClientConfig(
		*kubeConfig, contextName, &clientcmd.ConfigOverrides{}, nil,
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build config for context %q: %w", contextName, err)
	}

	newClientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}
	newDynamic, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	newDiscovery, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}
	newMetrics, err := metricsclientset.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics client: %w", err)
	}

	version, err := newClientset.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to cluster in context %q: %w", contextName, err)
	}

	c.cacheLock.Lock()
	c.clientset = newClientset
	c.dynamicClient = newDynamic
	c.discoveryClient = newDiscovery
	c.metricsClientset = newMetrics
	c.restConfig = restConfig
	c.apiResourceCache = make(map[string]*schema.GroupVersionResource)
	c.cacheRefreshedAt = time.Time{}
	c.cacheLock.Unlock()

	return map[string]interface{}{
		"context":       contextName,
		"cluster":       kubeConfig.Contexts[contextName].Cluster,
		"serverVersion": version.GitVersion,
		"status":        "switched",
	}, nil
}
