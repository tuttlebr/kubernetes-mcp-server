package k8s

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSanitizeResource_SecretRedaction(t *testing.T) {
	content := map[string]interface{}{
		"kind": "Secret",
		"metadata": map[string]interface{}{
			"name":      "my-secret",
			"namespace": "default",
		},
		"data": map[string]interface{}{
			"password": base64.StdEncoding.EncodeToString([]byte("supersecret")),
			"token":    base64.StdEncoding.EncodeToString([]byte("mytoken123")),
		},
		"stringData": map[string]interface{}{
			"config": "plain-text-value",
		},
	}

	result := SanitizeResource(content)

	data := result["data"].(map[string]interface{})
	for key, val := range data {
		s := val.(string)
		if !strings.HasPrefix(s, "[REDACTED") {
			t.Errorf("Secret data[%s] was not redacted: %s", key, s)
		}
	}

	stringData := result["stringData"].(map[string]interface{})
	for key, val := range stringData {
		s := val.(string)
		if !strings.HasPrefix(s, "[REDACTED") {
			t.Errorf("Secret stringData[%s] was not redacted: %s", key, s)
		}
	}
}

func TestSanitizeResource_ConfigMapBinaryData(t *testing.T) {
	content := map[string]interface{}{
		"kind": "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "my-cm",
		},
		"binaryData": map[string]interface{}{
			"cert.pem": base64.StdEncoding.EncodeToString(make([]byte, 4096)),
		},
		"data": map[string]interface{}{
			"config.yaml": "key: value",
		},
	}

	result := SanitizeResource(content)

	// binaryData should be redacted
	bd := result["binaryData"].(map[string]interface{})
	s := bd["cert.pem"].(string)
	if !strings.HasPrefix(s, "[REDACTED") {
		t.Errorf("ConfigMap binaryData was not redacted: %s", s)
	}

	// Regular data should be preserved
	d := result["data"].(map[string]interface{})
	if d["config.yaml"] != "key: value" {
		t.Error("ConfigMap data was incorrectly modified")
	}
}

func TestSanitizeResource_LastAppliedConfigRemoved(t *testing.T) {
	content := map[string]interface{}{
		"kind": "Deployment",
		"metadata": map[string]interface{}{
			"name": "my-deploy",
			"annotations": map[string]interface{}{
				"kubectl.kubernetes.io/last-applied-configuration": `{"very":"large","json":"blob"}`,
				"custom-annotation": "keep-me",
			},
		},
	}

	result := SanitizeResource(content)

	meta := result["metadata"].(map[string]interface{})
	annotations := meta["annotations"].(map[string]interface{})

	if _, exists := annotations["kubectl.kubernetes.io/last-applied-configuration"]; exists {
		t.Error("last-applied-configuration annotation was not removed")
	}
	if annotations["custom-annotation"] != "keep-me" {
		t.Error("custom annotation was incorrectly removed")
	}
}

func TestSanitizeResource_ManagedFieldsRemoved(t *testing.T) {
	content := map[string]interface{}{
		"kind": "Pod",
		"metadata": map[string]interface{}{
			"name":          "my-pod",
			"managedFields": []interface{}{"field1", "field2"},
			"selfLink":      "/api/v1/pods/my-pod",
		},
	}

	result := SanitizeResource(content)
	meta := result["metadata"].(map[string]interface{})

	if _, exists := meta["managedFields"]; exists {
		t.Error("managedFields was not removed")
	}
	if _, exists := meta["selfLink"]; exists {
		t.Error("selfLink was not removed")
	}
}

func TestSanitizeResource_DeepBase64Detection(t *testing.T) {
	// Create a large base64 string that doesn't live in Secret.data
	rawCert := make([]byte, 2048)
	for i := range rawCert {
		rawCert[i] = byte(i % 256)
	}
	b64Cert := base64.StdEncoding.EncodeToString(rawCert)

	content := map[string]interface{}{
		"kind": "CustomResource",
		"metadata": map[string]interface{}{
			"name": "my-cr",
		},
		"spec": map[string]interface{}{
			"tlsCert": b64Cert,
			"name":    "normal-string",
			"nested": map[string]interface{}{
				"caBundle": b64Cert,
			},
		},
	}

	result := SanitizeResource(content)

	spec := result["spec"].(map[string]interface{})
	if !strings.Contains(spec["tlsCert"].(string), "BASE64_DATA") {
		t.Error("large base64 in spec.tlsCert was not detected")
	}
	if spec["name"] != "normal-string" {
		t.Error("normal string was incorrectly modified")
	}

	nested := spec["nested"].(map[string]interface{})
	if !strings.Contains(nested["caBundle"].(string), "BASE64_DATA") {
		t.Error("large base64 in spec.nested.caBundle was not detected")
	}
}

func TestSanitizeResource_PreservesShortStrings(t *testing.T) {
	content := map[string]interface{}{
		"kind": "Service",
		"metadata": map[string]interface{}{
			"name": "my-svc",
		},
		"spec": map[string]interface{}{
			"clusterIP": "10.0.0.1",
			"type":      "ClusterIP",
		},
	}

	result := SanitizeResource(content)
	spec := result["spec"].(map[string]interface{})

	if spec["clusterIP"] != "10.0.0.1" {
		t.Error("short string clusterIP was incorrectly modified")
	}
	if spec["type"] != "ClusterIP" {
		t.Error("short string type was incorrectly modified")
	}
}

func TestSanitizeText_InlineBase64(t *testing.T) {
	blob := base64.StdEncoding.EncodeToString(make([]byte, 512))
	input := "apiVersion: v1\nkind: Secret\ndata:\n  password: " + blob + "\n  name: test\n"

	result := SanitizeText(input)

	if strings.Contains(result, blob) {
		t.Error("inline base64 blob was not replaced")
	}
	if !strings.Contains(result, "BASE64_DATA") {
		t.Error("placeholder was not inserted")
	}
	if !strings.Contains(result, "name: test") {
		t.Error("non-base64 lines were incorrectly modified")
	}
}

func TestSanitizeText_Truncation(t *testing.T) {
	// Create a string larger than maxResponseBytes
	large := strings.Repeat("a", maxResponseBytes+1000)
	result := SanitizeText(large)

	if len(result) > maxResponseBytes+200 { // allow room for the truncation message
		t.Errorf("output was not truncated: got %d bytes", len(result))
	}
	if !strings.Contains(result, "TRUNCATED") {
		t.Error("truncation message was not added")
	}
}

func TestTruncateJSON(t *testing.T) {
	small := []byte(`{"key":"value"}`)
	if string(TruncateJSON(small)) != string(small) {
		t.Error("small JSON was incorrectly truncated")
	}

	large := make([]byte, maxResponseBytes+1)
	for i := range large {
		large[i] = 'x'
	}
	result := TruncateJSON(large)
	if len(result) >= len(large) {
		t.Error("large JSON was not truncated")
	}
	if !strings.Contains(string(result), "response too large") {
		t.Error("truncation error message not present")
	}
}

func TestLooksLikeText(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect bool
	}{
		{"JSON", `{"key": "value", "number": 42}`, true},
		{"YAML", "apiVersion: v1\nkind: Pod\nmetadata:\n  name: test", true},
		{"prose", "This is a normal sentence with spaces and punctuation.", true},
		{"base64", "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo=", false},
		{"long base64", strings.Repeat("ABCD", 100), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeText(tt.input)
			if got != tt.expect {
				t.Errorf("looksLikeText(%q) = %v, want %v", tt.input[:min(len(tt.input), 40)], got, tt.expect)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
