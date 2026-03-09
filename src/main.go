// Package main is the entry point for the Kubernetes MCP server.
// Manage Kubernetes Cluster workloads via MCP.
// It initializes the MCP server, sets up the Kubernetes client,
// and registers the necessary handlers for various Kubernetes operations.
// It also starts the server to listen for incoming requests via stdio, SSE, or streamable-http transport.
// It uses the MCP Go library to create the server and handle requests.
// The server is capable of handling various Kubernetes operations
// such as listing resources, getting resource details, and retrieving logs.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/reza-gholizade/k8s-mcp-server/handlers"
	"github.com/reza-gholizade/k8s-mcp-server/pkg/helm"
	"github.com/reza-gholizade/k8s-mcp-server/pkg/k8s"
	"github.com/reza-gholizade/k8s-mcp-server/tools"

	"github.com/mark3labs/mcp-go/server"
)

// loggingMiddleware wraps an http.Handler and logs each request
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response wrapper to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Call the next handler
		next.ServeHTTP(rw, r)

		// Log the request
		log.Printf("[%s] %s %s - %d (%s)",
			r.Method,
			r.URL.Path,
			r.RemoteAddr,
			rw.statusCode,
			time.Since(start),
		)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code
// while preserving Flusher/Hijacker interfaces required for SSE streaming.
type responseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.wroteHeader = true
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// main initializes the Kubernetes client, sets up the MCP server with
// Kubernetes tool handlers, and starts the server in the configured mode.
func main() {
	// Parse command line flags
	var mode string
	var port string
	var readOnly bool
	var noK8s bool
	var noHelm bool

	flag.StringVar(&port, "port", getEnvOrDefault("SERVER_PORT", "8080"), "Server port")
	flag.StringVar(&mode, "mode", getEnvOrDefault("SERVER_MODE", "sse"), "Server mode: 'stdio', 'sse', or 'streamable-http'")
	flag.BoolVar(&readOnly, "read-only", false, "Enable read-only mode (disables write operations)")
	flag.BoolVar(&noK8s, "no-k8s", false, "Disable Kubernetes tools")
	flag.BoolVar(&noHelm, "no-helm", false, "Disable Helm tools")
	flag.Parse()

	// Validate flag combinations
	if noK8s && noHelm {
		fmt.Println("Error: Cannot disable both Kubernetes and Helm tools. At least one tool category must be enabled.")
		os.Exit(1)
	}

	// Log read-only mode status
	if readOnly {
		fmt.Println("Starting server in read-only mode - write operations disabled")
	}

	// Log disabled tool categories
	if noK8s {
		fmt.Println("Kubernetes tools disabled")
	}
	if noHelm {
		fmt.Println("Helm tools disabled")
	}

	// Create MCP server
	s := server.NewMCPServer(
		"MCP K8S & Helm Server",
		"1.0.0",
		server.WithResourceCapabilities(true, true), // Enable resource listing and subscription capabilities
	)

	// Create a Kubernetes client
	client, err := k8s.NewClient("")
	if err != nil {
		fmt.Printf("Failed to create Kubernetes client: %v\n", err)
		return
	}

	// Create Helm client with default kubeconfig path
	helmClient, err := helm.NewClient("")
	if err != nil {
		fmt.Printf("Failed to create Helm client: %v\n", err)
		return
	}

	// Register Kubernetes tools
	if !noK8s {
		s.AddTool(tools.GetAPIResourcesTool(), handlers.GetAPIResources(client))
		s.AddTool(tools.ListResourcesTool(), handlers.ListResources(client))
		s.AddTool(tools.GetResourcesTool(), handlers.GetResources(client))
		s.AddTool(tools.DescribeResourcesTool(), handlers.DescribeResources(client))
		s.AddTool(tools.GetPodsLogsTools(), handlers.GetPodsLogs(client))
		s.AddTool(tools.GetNodeMetricsTools(), handlers.GetNodeMetrics(client))
		s.AddTool(tools.GetPodMetricsTool(), handlers.GetPodMetrics(client))
		s.AddTool(tools.GetEventsTool(), handlers.GetEvents(client))
		s.AddTool(tools.GetIngressesTool(), handlers.GetIngresses(client))

		// Enhanced Resource Inspection Tools (Read-Only)
		s.AddTool(tools.GetResourceYAMLTool(), handlers.GetResourceYAML(client))
		s.AddTool(tools.GetResourceDiffTool(), handlers.GetResourceDiff(client))
		s.AddTool(tools.GetNamespaceResourcesTool(), handlers.GetNamespaceResources(client))
		s.AddTool(tools.GetResourceOwnersTool(), handlers.GetResourceOwners(client))

		// Advanced Monitoring & Observability Tools (Read-Only)
		s.AddTool(tools.GetClusterHealthTool(), handlers.GetClusterHealth(client))
		s.AddTool(tools.GetResourceQuotasTool(), handlers.GetResourceQuotas(client))
		s.AddTool(tools.GetLimitRangesTool(), handlers.GetLimitRanges(client))
		s.AddTool(tools.GetTopPodsTool(), handlers.GetTopPods(client))
		s.AddTool(tools.GetTopNodesTool(), handlers.GetTopNodes(client))

		// Debugging & Troubleshooting Tools (Read-Only)
		s.AddTool(tools.GetPodDebugInfoTool(), handlers.GetPodDebugInfo(client))
		s.AddTool(tools.GetServiceEndpointsTool(), handlers.GetServiceEndpoints(client))
		s.AddTool(tools.GetNetworkPoliciesTool(), handlers.GetNetworkPolicies(client))
		s.AddTool(tools.GetSecurityContextTool(), handlers.GetSecurityContext(client))
		s.AddTool(tools.GetResourceHistoryTool(), handlers.GetResourceHistory(client))
		s.AddTool(tools.ValidateManifestTool(), handlers.ValidateManifest(client))

		// Namespace & cluster navigation (Read-Only)
		s.AddTool(tools.ListNamespacesTool(), handlers.ListNamespaces(client))
		s.AddTool(tools.GetRolloutStatusTool(), handlers.GetRolloutStatus(client))
		s.AddTool(tools.ListContextsTool(), handlers.ListContexts(client))

		if !readOnly {
			s.AddTool(tools.CreateOrUpdateResourceJSONTool(), handlers.CreateOrUpdateResourceJSON(client))
			s.AddTool(tools.CreateOrUpdateResourceYAMLTool(), handlers.CreateOrUpdateResourceYAML(client))
			s.AddTool(tools.DeleteResourceTool(), handlers.DeleteResource(client))
			s.AddTool(tools.RolloutRestartTool(), handlers.RolloutRestart(client))
			s.AddTool(tools.ExecInPodTool(), handlers.ExecInPod(client))
			s.AddTool(tools.ScaleResourceTool(), handlers.ScaleResource(client))
			s.AddTool(tools.SwitchContextTool(), handlers.SwitchContext(client))
		}
	}

	// Register Helm tools
	if !noHelm {
		s.AddTool(tools.HelmListTool(), handlers.HelmList(helmClient))
		s.AddTool(tools.HelmGetTool(), handlers.HelmGet(helmClient))
		s.AddTool(tools.HelmHistoryTool(), handlers.HelmHistory(helmClient))
		s.AddTool(tools.HelmRepoListTool(), handlers.HelmRepoList(helmClient))

		// Register write operations only if not in read-only mode
		if !readOnly {
			s.AddTool(tools.HelmInstallTool(), handlers.HelmInstall(helmClient))
			s.AddTool(tools.HelmUpgradeTool(), handlers.HelmUpgrade(helmClient))
			s.AddTool(tools.HelmUninstallTool(), handlers.HelmUninstall(helmClient))
			s.AddTool(tools.HelmRollbackTool(), handlers.HelmRollback(helmClient))
			s.AddTool(tools.HelmRepoAddTool(), handlers.HelmRepoAdd(helmClient))
		}
	}

	// Set up signal handling for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch mode {
	case "stdio":
		fmt.Println("Starting server in stdio mode...")
		if err := server.ServeStdio(s); err != nil {
			fmt.Printf("Failed to start stdio server: %v\n", err)
			return
		}
	case "sse":
		fmt.Printf("Starting server in SSE mode on port %s...\n", port)
		httpServer := &http.Server{
			Addr:              ":" + port,
			ReadHeaderTimeout: 30 * time.Second,
		}
		sse := server.NewSSEServer(s,
			server.WithHTTPServer(httpServer),
			server.WithKeepAliveInterval(30*time.Second),
		)

		baseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				handleHealth(w, r, client)
				return
			}
			sse.ServeHTTP(w, r)
		})

		httpServer.Handler = loggingMiddleware(baseHandler)

		go func() {
			if err := sse.Start(":" + port); err != nil && err != http.ErrServerClosed {
				fmt.Printf("SSE server error: %v\n", err)
			}
		}()
		fmt.Printf("SSE server started on port %s\n", port)

		<-ctx.Done()
		fmt.Println("\nShutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			fmt.Printf("Server shutdown error: %v\n", err)
		}

	case "streamable-http":
		fmt.Printf("Starting server in streamable-http mode on port %s...\n", port)
		httpServer := &http.Server{
			Addr:              ":" + port,
			ReadHeaderTimeout: 30 * time.Second,
		}
		streamableHTTP := server.NewStreamableHTTPServer(
			s,
			server.WithStreamableHTTPServer(httpServer),
			server.WithHeartbeatInterval(30*time.Second),
		)

		baseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				handleHealth(w, r, client)
				return
			}
			streamableHTTP.ServeHTTP(w, r)
		})

		httpServer.Handler = loggingMiddleware(baseHandler)

		go func() {
			if err := streamableHTTP.Start(":" + port); err != nil && err != http.ErrServerClosed {
				fmt.Printf("Streamable-http server error: %v\n", err)
			}
		}()
		fmt.Printf("Streamable-http server started on port %s (endpoint: http://localhost:%s/mcp)\n", port, port)

		<-ctx.Done()
		fmt.Println("\nShutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			fmt.Printf("Server shutdown error: %v\n", err)
		}

	default:
		fmt.Printf("Unknown server mode: %s. Use 'stdio', 'sse', or 'streamable-http'.\n", mode)
		return
	}

	fmt.Println("Server stopped gracefully")
}

// getEnvOrDefault returns the value of the environment variable or the default value if not set
func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// handleHealth verifies K8s API connectivity and returns 200 if healthy.
func handleHealth(w http.ResponseWriter, r *http.Request, client *k8s.Client) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if err := client.CheckConnection(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(fmt.Sprintf("unhealthy: %v", err)))
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
