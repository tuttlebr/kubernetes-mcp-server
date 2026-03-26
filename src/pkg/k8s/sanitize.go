package k8s

import "fmt"

// SanitizeResource redacts sensitive fields from a Kubernetes resource
// represented as unstructured content. It:
//   - Replaces all values in Secret `data` and `stringData` fields with [REDACTED]
//   - Preserves the key names so the LLM knows which fields exist
func SanitizeResource(content map[string]interface{}) map[string]interface{} {
	if content == nil {
		return content
	}

	kind, _ := content["kind"].(string)
	if kind == "Secret" {
		redactMapValues(content, "data")
		redactMapValues(content, "stringData")
	}

	return content
}

// redactMapValues replaces all values in content[field] with a redacted placeholder.
func redactMapValues(content map[string]interface{}, field string) {
	dataMap, ok := content[field].(map[string]interface{})
	if !ok {
		return
	}
	for k, v := range dataMap {
		if s, ok := v.(string); ok {
			dataMap[k] = fmt.Sprintf("[REDACTED %d bytes]", len(s))
		} else {
			dataMap[k] = "[REDACTED]"
		}
	}
}
