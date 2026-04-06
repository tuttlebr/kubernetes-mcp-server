package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config holds the configuration for the opencode agent runner.
type Config struct {
	BaseURL        string
	APIKey         string
	Model          string
	BinaryPath     string
	KubeconfigPath string
}

// Client orchestrates headless opencode runs for autonomous Kubernetes DevOps operations.
type Client struct {
	config Config
}

// NewClient creates a new agent Client from environment variables and the current binary path.
func NewClient(kubeconfigPath string) (*Client, error) {
	baseURL := os.Getenv("OPENCODE_BASE_URL")
	apiKey := os.Getenv("OPENCODE_API_KEY")
	model := os.Getenv("OPENCODE_MODEL")

	if baseURL == "" {
		return nil, fmt.Errorf("OPENCODE_BASE_URL environment variable is required")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("OPENCODE_API_KEY environment variable is required")
	}
	if model == "" {
		return nil, fmt.Errorf("OPENCODE_MODEL environment variable is required (format: provider/model)")
	}

	binaryPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve executable path: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve executable symlinks: %w", err)
	}

	return &Client{
		config: Config{
			BaseURL:        baseURL,
			APIKey:         apiKey,
			Model:          model,
			BinaryPath:     binaryPath,
			KubeconfigPath: kubeconfigPath,
		},
	}, nil
}

// RunParams contains parameters for a single DevOps agent run.
type RunParams struct {
	Prompt    string
	Namespace string
	Model     string
	Timeout   int
	ReadOnly  bool
}

// Run executes opencode headlessly with the given DevOps task prompt.
func (c *Client) Run(ctx context.Context, params RunParams) (map[string]interface{}, error) {
	opencodePath, err := exec.LookPath("opencode")
	if err != nil {
		return nil, fmt.Errorf("opencode CLI not found in PATH; install via: npm install -g opencode-ai")
	}

	timeout := params.Timeout
	if timeout <= 0 {
		timeout = 300
	}
	if timeout > 900 {
		timeout = 900
	}

	tmpDir, err := os.MkdirTemp("", "opencode-agent-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "opencode.json")
	if err := c.writeConfig(configPath, params); err != nil {
		return nil, fmt.Errorf("failed to write opencode config: %w", err)
	}

	fullPrompt := c.buildPrompt(params)

	model := c.config.Model
	if params.Model != "" {
		model = params.Model
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, opencodePath, "run", "--format", "json", "--model", model)
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(),
		"OPENCODE_BASE_URL="+c.config.BaseURL,
		"OPENCODE_API_KEY="+c.config.APIKey,
	)
	cmd.Stdin = strings.NewReader(fullPrompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	result := map[string]interface{}{
		"prompt": params.Prompt,
		"model":  model,
	}

	if stderr.Len() > 0 {
		result["stderr"] = stderr.String()
	}

	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			result["error"] = fmt.Sprintf("agent timed out after %d seconds", timeout)
		} else {
			result["error"] = err.Error()
		}
		result["exitCode"] = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			result["exitCode"] = exitErr.ExitCode()
		}
	} else {
		result["exitCode"] = 0
	}

	agentOutput := c.parseNDJSON(stdout.Bytes())
	result["output"] = agentOutput

	return result, nil
}

// writeConfig generates the opencode.json configuration file.
func (c *Client) writeConfig(configPath string, params RunParams) error {
	childArgs := []interface{}{"--mode", "stdio"}
	if params.ReadOnly {
		childArgs = append(childArgs, "--read-only")
	}

	command := []interface{}{c.config.BinaryPath}
	command = append(command, childArgs...)

	environment := map[string]string{}
	if c.config.KubeconfigPath != "" {
		environment["KUBECONFIG"] = c.config.KubeconfigPath
	}

	providerID, modelID := parseModel(c.config.Model)
	if params.Model != "" {
		providerID, modelID = parseModel(params.Model)
	}

	config := map[string]interface{}{
		"provider": map[string]interface{}{
			providerID: map[string]interface{}{
				"npm": "@ai-sdk/openai-compatible",
				"models": map[string]interface{}{
					modelID: map[string]interface{}{
						"name":      modelID,
						"tool_call": true,
					},
				},
				"options": map[string]interface{}{
					"baseURL": c.config.BaseURL,
					"apiKey":  c.config.APIKey,
				},
			},
		},
		"mcp": map[string]interface{}{
			"k8s": map[string]interface{}{
				"type":        "local",
				"command":     command,
				"environment": environment,
			},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(configPath, data, 0600)
}

// buildPrompt constructs the full prompt including system instructions.
func (c *Client) buildPrompt(params RunParams) string {
	var sb strings.Builder

	sb.WriteString("You are an autonomous Kubernetes DevOps agent. ")
	sb.WriteString("You have access to a comprehensive set of Kubernetes and Helm MCP tools for cluster management, deployment, and troubleshooting. ")
	sb.WriteString("Use them systematically to accomplish the task described below.\n\n")

	sb.WriteString("STRATEGY:\n")
	sb.WriteString("1. Assess the task: determine if it is an installation, upgrade, scaling, debugging, or general management operation\n")
	sb.WriteString("2. For management tasks: use Helm tools (helmInstall, helmUpgrade) and resource mutation tools (createOrUpdateResourceJSON, createOrUpdateResourceYAML, scaleResource, rolloutRestart) as needed\n")
	sb.WriteString("3. For debugging tasks: start broad (getClusterHealth, getClusterSummary, getEvents), then drill down (describeResource, getPodsLogs, getPodDebugInfo)\n")
	sb.WriteString("4. Verify outcomes: after any mutation, confirm the desired state (getRolloutStatus, listResources, getEvents)\n")
	sb.WriteString("5. For GPU workloads, use dedicated GPU tools (getGPUClusterOverview, diagnoseGPUScheduling, getGPUOperatorHealth)\n\n")

	if params.ReadOnly {
		sb.WriteString("MODE: READ-ONLY. You may only inspect and diagnose. Do NOT attempt any write operations.\n\n")
	} else {
		sb.WriteString("MODE: READ-WRITE. You may perform remediation actions if you are confident in the fix.\n\n")
	}

	if params.Namespace != "" {
		sb.WriteString(fmt.Sprintf("FOCUS NAMESPACE: %s\n\n", params.Namespace))
	}

	sb.WriteString("REQUIRED OUTPUT FORMAT:\n")
	sb.WriteString("Provide a structured summary with:\n")
	sb.WriteString("- **Objective**: What was requested\n")
	sb.WriteString("- **Actions Taken**: What operations were performed and their results\n")
	sb.WriteString("- **Current State**: The resulting cluster state after the operations\n")
	sb.WriteString("- **Issues Found**: Any problems encountered (if applicable)\n")
	sb.WriteString("- **Next Steps**: Recommended follow-up actions (if any)\n\n")

	sb.WriteString("USER REQUEST:\n")
	sb.WriteString(params.Prompt)

	return sb.String()
}

// parseNDJSON extracts the assistant's text output from opencode's NDJSON stream.
func (c *Client) parseNDJSON(data []byte) string {
	var textParts []string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		if eventType != "text" {
			continue
		}

		// Extract text from part object: {"type":"text","part":{"type":"text","text":"..."}}
		if part, ok := event["part"].(map[string]interface{}); ok {
			if text, ok := part["text"].(string); ok && text != "" {
				textParts = append(textParts, text)
			}
		}
	}

	if len(textParts) == 0 {
		return string(data)
	}

	return strings.Join(textParts, "")
}

// parseModel splits a "provider/model" string into its components.
func parseModel(model string) (providerID, modelID string) {
	if parts := strings.SplitN(model, "/", 2); len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "openai-compat", model
}
