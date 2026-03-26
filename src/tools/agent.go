package tools

import (
	"github.com/mark3labs/mcp-go/mcp"
)

// AgentDebugTool creates a tool for autonomous Kubernetes debugging using an AI agent.
func AgentDebugTool() mcp.Tool {
	return mcp.NewTool(
		"agentDebug",
		mcp.WithDescription("Launch an autonomous AI debugging agent that investigates Kubernetes issues "+
			"using the full suite of k8s and Helm MCP tools. The agent runs headlessly via opencode, "+
			"connecting to a child k8s-mcp-server instance for cluster access. "+
			"Provide a natural language description of the issue and the agent will systematically "+
			"inspect cluster state, logs, events, and resource configurations to produce a structured diagnosis. "+
			"Requires: opencode CLI installed, OPENCODE_BASE_URL, OPENCODE_API_KEY, and OPENCODE_MODEL env vars set. "+
			"WRITE OPERATION: only available when server is not in read-only mode."),
		mcp.WithString("prompt", mcp.Required(),
			mcp.Description("Natural language description of the Kubernetes issue to debug. "+
				"Be specific: include resource names, namespaces, error messages, or symptoms. "+
				"Examples: "+
				"\"Pods in namespace ml-training are stuck in Pending state with GPU resource requests\", "+
				"\"The nginx-ingress deployment in production keeps crashing with OOMKilled\", "+
				"\"Services in namespace api-gateway are not reachable from other namespaces\".")),
		mcp.WithString("namespace",
			mcp.Description("Kubernetes namespace to focus the investigation on. "+
				"The agent will prioritize resources in this namespace but may inspect cluster-wide resources as needed.")),
		mcp.WithString("model",
			mcp.Description("Override the default LLM model for this agent run. "+
				"Format: \"provider-id/model-id\" matching the opencode provider configuration. "+
				"Uses OPENCODE_MODEL environment variable if omitted.")),
		mcp.WithNumber("timeout",
			mcp.Description("Maximum execution time in seconds for the agent run. "+
				"Agentic debugging tasks typically take 1-5 minutes. Default: 300. Max: 900."),
			mcp.DefaultNumber(300)),
		mcp.WithBoolean("readOnly",
			mcp.Description("When true (default), the child k8s-mcp-server runs in read-only mode, "+
				"preventing the agent from making any cluster changes. "+
				"Set to false to allow the agent to perform remediation actions."),
			mcp.DefaultBool(true)),
	)
}
