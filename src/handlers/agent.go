package handlers

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/reza-gholizade/k8s-mcp-server/pkg/agent"
)

// AgentDebug returns a handler function for the agentDebug tool.
func AgentDebug(agentClient *agent.Client) func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, ok := request.Params.Arguments.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid arguments type: expected map[string]interface{}")
		}

		prompt, err := getRequiredStringArg(args, "prompt")
		if err != nil {
			return nil, err
		}

		namespace := getStringArg(args, "namespace", "")
		model := getStringArg(args, "model", "")
		readOnly := getBoolArg(args, "readOnly", true)

		timeout := 300
		if val, ok := args["timeout"]; ok {
			switch v := val.(type) {
			case float64:
				timeout = int(v)
			case int:
				timeout = v
			}
		}

		params := agent.RunParams{
			Prompt:    prompt,
			Namespace: namespace,
			Model:     model,
			Timeout:   timeout,
			ReadOnly:  readOnly,
		}

		result, err := agentClient.Run(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("agent debugging failed: %w", err)
		}

		jsonResponse, err := marshalSafe(result)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize agent response: %w", err)
		}

		return mcp.NewToolResultText(string(jsonResponse)), nil
	}
}
