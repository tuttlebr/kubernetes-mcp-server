package tools

import (
	"github.com/mark3labs/mcp-go/mcp"
)

// HelmInstallTool returns the MCP tool definition for installing Helm charts.
func HelmInstallTool() mcp.Tool {
	return mcp.NewTool("helmInstall",
		mcp.WithDescription("Install a Helm chart to the Kubernetes cluster, creating a new release. "+
			"Equivalent to 'helm install'. The chart can be referenced by name from a configured repo "+
			"(e.g. \"nginx/nginx-ingress\") or by a local path. "+
			"Use helmList to check if a release already exists — use helmUpgrade for existing releases. "+
			"WRITE OPERATION: only available when server is not in read-only mode."),
		mcp.WithString("releaseName", mcp.Required(),
			mcp.Description("Name for the Helm release. Must be unique within the namespace. "+
				"Example: \"my-nginx\", \"prometheus-stack\".")),
		mcp.WithString("chartName", mcp.Required(),
			mcp.Description("Chart reference: either \"repo/chart\" for a repository chart (e.g. \"bitnami/nginx\") "+
				"or a local filesystem path to a chart directory/archive.")),
		mcp.WithString("namespace",
			mcp.Description("Kubernetes namespace to install the release into. "+
				"The namespace must already exist. Defaults to \"default\" if omitted.")),
		mcp.WithString("repoURL",
			mcp.Description("Helm repository URL to add before installing. "+
				"Only needed if the chart's repository has not been previously added via helmRepoAdd. "+
				"Example: \"https://charts.bitnami.com/bitnami\".")),
		mcp.WithObject("values",
			mcp.Description("Key-value pairs to override chart default values, equivalent to 'helm install --set' or '-f values.yaml'. "+
				"Pass as a JSON object. Example: {\"replicaCount\": 3, \"image.tag\": \"latest\"}.")),
	)
}

// HelmUpgradeTool returns the MCP tool definition for upgrading Helm releases.
func HelmUpgradeTool() mcp.Tool {
	return mcp.NewTool("helmUpgrade",
		mcp.WithDescription("Upgrade an existing Helm release to a new chart version or with new values. "+
			"Equivalent to 'helm upgrade'. The release must already exist — use helmInstall for new releases. "+
			"Use helmHistory to see previous revisions and helmRollback to revert if needed. "+
			"WRITE OPERATION: only available when server is not in read-only mode."),
		mcp.WithString("releaseName", mcp.Required(),
			mcp.Description("Name of the existing Helm release to upgrade.")),
		mcp.WithString("chartName", mcp.Required(),
			mcp.Description("Chart reference: either \"repo/chart\" for a repository chart (e.g. \"bitnami/nginx\") "+
				"or a local filesystem path to a chart directory/archive.")),
		mcp.WithString("namespace",
			mcp.Description("Kubernetes namespace where the release is installed. Defaults to \"default\" if omitted.")),
		mcp.WithObject("values",
			mcp.Description("Key-value pairs to override chart values. Replaces previously set values. "+
				"Pass as a JSON object. Example: {\"replicaCount\": 5, \"image.tag\": \"v2.0\"}.")),
		mcp.WithString("repoURL",
			mcp.Description("Helm repository URL. Only needed if the chart's repository has not been previously added. "+
				"Example: \"https://prometheus-community.github.io/helm-charts\".")),
	)
}

// HelmUninstallTool returns the MCP tool definition for uninstalling Helm releases.
func HelmUninstallTool() mcp.Tool {
	return mcp.NewTool("helmUninstall",
		mcp.WithDescription("Uninstall a Helm release, removing all associated Kubernetes resources. "+
			"Equivalent to 'helm uninstall'. This is a destructive operation that cannot be undone. "+
			"Use helmGet to inspect the release before uninstalling. "+
			"WRITE OPERATION: only available when server is not in read-only mode."),
		mcp.WithString("releaseName", mcp.Required(),
			mcp.Description("Exact name of the Helm release to uninstall. Use helmList to discover release names.")),
		mcp.WithString("namespace", mcp.Required(),
			mcp.Description("Kubernetes namespace where the release is installed.")),
	)
}

// HelmListTool returns the MCP tool definition for listing Helm releases.
func HelmListTool() mcp.Tool {
	return mcp.NewTool("helmList",
		mcp.WithDescription("List Helm releases with their status, chart version, app version, and last update time. "+
			"Equivalent to 'helm list'. Use this to discover installed releases before running helmGet, helmUpgrade, or helmUninstall."),
		mcp.WithString("namespace", mcp.Required(),
			mcp.Description("Kubernetes namespace to list releases from. Pass an empty string \"\" to list releases across all namespaces.")),
	)
}

// HelmGetTool returns the MCP tool definition for getting Helm release details.
func HelmGetTool() mcp.Tool {
	return mcp.NewTool("helmGet",
		mcp.WithDescription("Get detailed information about a specific Helm release, including its chart, values, "+
			"and manifest. Equivalent to 'helm get all'. "+
			"For release revision history, use helmHistory instead."),
		mcp.WithString("releaseName", mcp.Required(),
			mcp.Description("Exact name of the Helm release.")),
		mcp.WithString("namespace", mcp.Required(),
			mcp.Description("Kubernetes namespace where the release is installed.")),
	)
}

// HelmHistoryTool returns the MCP tool definition for getting Helm release history.
func HelmHistoryTool() mcp.Tool {
	return mcp.NewTool("helmHistory",
		mcp.WithDescription("Get the revision history of a Helm release, showing all past upgrades and rollbacks. "+
			"Equivalent to 'helm history'. Shows revision number, status, chart version, and description for each revision. "+
			"Use the revision number with helmRollback to revert to a previous version."),
		mcp.WithString("releaseName", mcp.Required(),
			mcp.Description("Exact name of the Helm release.")),
		mcp.WithString("namespace", mcp.Required(),
			mcp.Description("Kubernetes namespace where the release is installed.")),
	)
}

// HelmRollbackTool returns the MCP tool definition for rolling back Helm releases.
func HelmRollbackTool() mcp.Tool {
	return mcp.NewTool("helmRollback",
		mcp.WithDescription("Rollback a Helm release to a previous revision. Equivalent to 'helm rollback'. "+
			"Use helmHistory first to see available revisions and their details. "+
			"WRITE OPERATION: only available when server is not in read-only mode."),
		mcp.WithString("releaseName", mcp.Required(),
			mcp.Description("Exact name of the Helm release to rollback.")),
		mcp.WithString("namespace", mcp.Required(),
			mcp.Description("Kubernetes namespace where the release is installed.")),
		mcp.WithNumber("revision", mcp.Required(),
			mcp.Description("Revision number to rollback to. Use 0 to rollback to the immediately previous revision. "+
				"Use helmHistory to discover available revision numbers."),
			mcp.Min(0)),
	)
}

// HelmRepoAddTool returns the MCP tool definition for adding Helm repositories.
func HelmRepoAddTool() mcp.Tool {
	return mcp.NewTool("helmRepoAdd",
		mcp.WithDescription("Add a Helm chart repository so that charts from it can be installed. "+
			"Equivalent to 'helm repo add'. After adding, use helmInstall with the repo name prefix (e.g. \"myrepo/mychart\"). "+
			"Use helmRepoList to see currently configured repositories. "+
			"WRITE OPERATION: only available when server is not in read-only mode."),
		mcp.WithString("repoName", mcp.Required(),
			mcp.Description("Local name for the repository. Used as a prefix when referencing charts (e.g. \"bitnami\"). "+
				"Example: \"bitnami\", \"prometheus-community\".")),
		mcp.WithString("repoURL", mcp.Required(),
			mcp.Description("URL of the Helm chart repository index. "+
				"Example: \"https://charts.bitnami.com/bitnami\", \"https://prometheus-community.github.io/helm-charts\".")),
	)
}

// HelmRepoListTool returns the MCP tool definition for listing configured Helm repositories.
func HelmRepoListTool() mcp.Tool {
	return mcp.NewTool("helmRepoList",
		mcp.WithDescription("List all configured Helm chart repositories with their names and URLs. "+
			"Equivalent to 'helm repo list'. Use this to verify which repositories are available "+
			"before installing charts with helmInstall."),
	)
}
