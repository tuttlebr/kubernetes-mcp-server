# Kubernetes & Helm MCP Server

A [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that exposes **43 tools** for managing Kubernetes clusters and Helm releases through any MCP-compatible client. Built in Go for low resource overhead, it supports multi-cluster context switching, read-only mode, and in-cluster or kubeconfig-based authentication.

## Table of Contents

- [Features](#features)
- [Architecture](#architecture)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Tool Reference](#tool-reference)
- [Deployment](#deployment)
- [Security](#security)
- [Development](#development)
- [License](#license)

## Features

**Kubernetes Operations** — 34 tools covering the full lifecycle of cluster resources:

| Category                   | Capabilities                                                                                                                                       |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Discovery & Inspection** | List API resources, get/describe/export resources as JSON or YAML, diff resource states, trace ownership chains                                    |
| **Monitoring**             | Cluster health checks, top pods/nodes by CPU or memory, resource quotas, limit ranges                                                              |
| **Debugging**              | Pod debug info (conditions + events + logs), service endpoint health, network policy analysis, security context inspection, resource event history |
| **Mutations**              | Create/update resources from JSON or YAML, delete resources, scale workloads, rollout restarts, exec in pods, manifest dry-run validation          |
| **Navigation**             | List namespaces, list/switch kubeconfig contexts, rollout status                                                                                   |

**Helm Operations** — 9 tools for chart lifecycle management:

| Category  | Capabilities                                                           |
| --------- | ---------------------------------------------------------------------- |
| **Read**  | List releases, get release details, release history, list repositories |
| **Write** | Install, upgrade, uninstall, rollback releases, add repositories       |

**Platform** — Flexible resource identifier resolution accepts PascalCase Kind (`Deployment`), lowercase plural (`deployments`), singular (`deployment`), or short names (`deploy`).

## Architecture

```
src/
├── main.go              # Server entry point, tool registration, transport setup
├── tools/
│   ├── k8s.go           # Kubernetes MCP tool definitions (schemas)
│   └── helm.go          # Helm MCP tool definitions (schemas)
├── handlers/
│   ├── k8s.go           # Kubernetes tool handlers (request → response)
│   └── helm.go          # Helm tool handlers (request → response)
└── pkg/
    ├── k8s/
    │   └── client.go    # Kubernetes client (dynamic, discovery, metrics, typed)
    └── helm/
        └── client.go    # Helm client (install, upgrade, release management)
```

The server uses a **dynamic Kubernetes client** with a cached GVR (GroupVersionResource) resolver backed by the discovery API, so it works with any resource type — including CRDs — without hardcoded mappings.

## Quick Start

### Prerequisites

- Go 1.24+ (for building from source)
- Access to a Kubernetes cluster with a valid kubeconfig
- [metrics-server](https://github.com/kubernetes-sigs/metrics-server) installed for CPU/memory metrics tools

### Build from Source

```bash
cd src
go mod download
CGO_ENABLED=0 go build -ldflags="-w -s" -o k8s-mcp-server main.go
```

### Run Locally

```bash
# Streamable HTTP (default) — endpoint: http://localhost:8080/mcp
./k8s-mcp-server

# SSE transport
./k8s-mcp-server --mode sse

# Stdio transport (for CLI/editor integrations)
./k8s-mcp-server --mode stdio

# Read-only mode (disables all write operations)
./k8s-mcp-server --read-only

# Disable Helm tools
./k8s-mcp-server --no-helm

# Combine flags
./k8s-mcp-server --mode sse --port 9090 --read-only --no-helm
```

### Docker

```bash
# Build
docker compose build

# Run with local kubeconfig
docker run -p 8080:8080 \
  -v ~/.kube/config:/home/appuser/.kube/config:ro \
  k8s-mcp-server:latest
```

### Docker Compose

```yaml
services:
  k8s-mcp-server:
    build: ./src
    ports:
      - "8080:8080"
    volumes:
      - ~/.kube:/home/appuser/.kube:ro
    environment:
      KUBECONFIG: /home/appuser/.kube/config
      SERVER_MODE: streamable-http
      SERVER_PORT: "8080"
    restart: unless-stopped
```

## Configuration

### Command-Line Flags

| Flag          | Environment Variable | Default | Description                                      |
| ------------- | -------------------- | ------- | ------------------------------------------------ |
| `--mode`      | `SERVER_MODE`        | `sse`   | Transport: `stdio`, `sse`, or `streamable-http`  |
| `--port`      | `SERVER_PORT`        | `8080`  | HTTP listen port (SSE and streamable-http modes) |
| `--read-only` | —                    | `false` | Disable all write operations                     |
| `--no-k8s`    | —                    | `false` | Disable all Kubernetes tools                     |
| `--no-helm`   | —                    | `false` | Disable all Helm tools                           |

`--no-k8s` and `--no-helm` cannot be used together.

### Kubeconfig Resolution Order

1. Explicit path via `KUBECONFIG` environment variable
2. In-cluster ServiceAccount config (when running inside Kubernetes)
3. `~/.kube/config` fallback

### Health Check

All HTTP modes expose `GET /healthz` which verifies connectivity to the Kubernetes API server. Returns `200 ok` or `503 unhealthy: <error>`.

## Tool Reference

### Kubernetes — Read-Only Tools (27)

#### Discovery & Core

| Tool                    | Description                                                 | Key Parameters                                          |
| ----------------------- | ----------------------------------------------------------- | ------------------------------------------------------- |
| `getAPIResources`       | List all available API resource types in the cluster        | `includeNamespaceScoped`, `includeClusterScoped`        |
| `listResources`         | List resources of a specific type, optionally filtered      | `kind`\*, `namespace`, `labelSelector`, `fieldSelector` |
| `getResource`           | Get full JSON of a single resource                          | `kind`\*, `name`\*, `namespace`                         |
| `describeResource`      | Human-friendly description with events and owner refs       | `kind`\*, `name`\*, `namespace`                         |
| `getResourceYAML`       | Export resource as clean YAML                               | `kind`\*, `name`\*, `namespace`                         |
| `getResourceDiff`       | Compare current state with previous or another resource     | `kind`\*, `name`\*, `namespace`, `compareWith`          |
| `getNamespaceResources` | List all resources in a namespace                           | `namespace`\*, `types`, `includeSecrets`                |
| `getResourceOwners`     | Trace ownership chain (e.g., Pod → ReplicaSet → Deployment) | `kind`\*, `name`\*, `namespace`, `includeChildren`      |
| `getIngresses`          | Find ingresses matching a hostname                          | `host`\*                                                |
| `listNamespaces`        | List all namespaces with status and labels                  | `labelSelector`                                         |
| `listContexts`          | List available kubeconfig contexts                          | —                                                       |

#### Monitoring & Observability

| Tool                | Description                                          | Key Parameters                    |
| ------------------- | ---------------------------------------------------- | --------------------------------- |
| `getClusterHealth`  | Cluster health report (nodes, control plane, events) | `includeMetrics`, `includeEvents` |
| `getTopPods`        | Top pods by CPU or memory usage                      | `namespace`, `sortBy`, `limit`    |
| `getTopNodes`       | Top nodes by resource utilization                    | `sortBy`, `includeConditions`     |
| `getNodeMetrics`    | CPU/memory metrics for a specific node               | `name`\*                          |
| `getPodMetrics`     | CPU/memory metrics for a specific pod                | `namespace`\*, `podName`\*        |
| `getResourceQuotas` | Resource quotas and usage                            | `namespace`, `showPercentage`     |
| `getLimitRanges`    | Limit ranges in namespaces                           | `namespace`                       |
| `getEvents`         | Cluster events, optionally filtered                  | `namespace`, `labelSelector`      |
| `getRolloutStatus`  | Rollout status for Deployment/StatefulSet/DaemonSet  | `kind`\*, `name`\*, `namespace`\* |

#### Debugging & Troubleshooting

| Tool                  | Description                                        | Key Parameters                                     |
| --------------------- | -------------------------------------------------- | -------------------------------------------------- |
| `getPodsLogs`         | Pod logs (single or all containers)                | `name`\*, `namespace`\*, `containerName`           |
| `getPodDebugInfo`     | Comprehensive pod debug (conditions, events, logs) | `name`\*, `namespace`\*, `includeLogs`, `logLines` |
| `getServiceEndpoints` | Service endpoints with pod health status           | `name`\*, `namespace`\*, `checkHealth`             |
| `getNetworkPolicies`  | Network policies affecting a namespace or pod      | `namespace`\*, `podName`, `includeDetails`         |
| `getSecurityContext`  | Pod and container security contexts                | `name`\*, `namespace`\*, `includeDefaults`         |
| `getResourceHistory`  | Recent events and changes for a resource           | `kind`\*, `name`\*, `namespace`, `hours`           |
| `validateManifest`    | Dry-run validation of YAML/JSON manifests          | `manifest`\*, `format`, `strict`                   |

### Kubernetes — Write Tools (7)

Disabled when `--read-only` is set.

| Tool                 | Description                               | Key Parameters                                    |
| -------------------- | ----------------------------------------- | ------------------------------------------------- |
| `createResource`     | Create/update resource from JSON manifest | `kind`\*, `manifest`\*, `namespace`               |
| `createResourceYAML` | Create/update resource from YAML manifest | `yamlManifest`\*, `kind`, `namespace`             |
| `deleteResource`     | Delete a resource by kind and name        | `kind`\*, `name`\*, `namespace`                   |
| `rolloutRestart`     | Rolling restart of a workload             | `kind`\*, `name`\*, `namespace`\*                 |
| `scaleResource`      | Scale a workload's replica count          | `kind`\*, `name`\*, `namespace`\*, `replicas`\*   |
| `execInPod`          | Execute a command in a pod container      | `name`\*, `namespace`\*, `command`\*, `container` |
| `switchContext`      | Switch the active kubeconfig context      | `context`\*                                       |

### Helm — Read-Only Tools (4)

| Tool           | Description                                   | Key Parameters                 |
| -------------- | --------------------------------------------- | ------------------------------ |
| `helmList`     | List Helm releases                            | `namespace`\*                  |
| `helmGet`      | Get release details (chart, values, manifest) | `releaseName`\*, `namespace`\* |
| `helmHistory`  | Release revision history                      | `releaseName`\*, `namespace`\* |
| `helmRepoList` | List configured chart repositories            | —                              |

### Helm — Write Tools (5)

Disabled when `--read-only` is set.

| Tool            | Description                     | Key Parameters                                                   |
| --------------- | ------------------------------- | ---------------------------------------------------------------- |
| `helmInstall`   | Install a Helm chart            | `releaseName`\*, `chartName`\*, `namespace`, `repoURL`, `values` |
| `helmUpgrade`   | Upgrade an existing release     | `releaseName`\*, `chartName`\*, `namespace`, `repoURL`, `values` |
| `helmUninstall` | Uninstall a release             | `releaseName`\*, `namespace`\*                                   |
| `helmRollback`  | Rollback to a previous revision | `releaseName`\*, `namespace`\*, `revision`\*                     |
| `helmRepoAdd`   | Add a chart repository          | `repoName`\*, `repoURL`\*                                        |

\* = required parameter

### Resource Identifier Flexibility

The `kind` parameter across all tools accepts multiple formats. All of these resolve to the same resource:

| Format             | Example       |
| ------------------ | ------------- |
| PascalCase Kind    | `Deployment`  |
| Lowercase plural   | `deployments` |
| Lowercase singular | `deployment`  |
| Short name         | `deploy`      |

This works for any resource type, including CRDs.

## Deployment

### Kubernetes In-Cluster

The included manifest at `deploy/k8s-mcp-server.yaml` provides a production-ready deployment:

```bash
# Apply namespace, RBAC, deployment, and service
kubectl apply -f deploy/k8s-mcp-server.yaml

# Create kubeconfig secret (for multi-cluster access)
kubectl -n k8s-mcp-server create secret generic k8s-mcp-kubeconfig \
  --from-file=config=$HOME/.kube/config \
  --dry-run=client -o yaml | kubectl apply -f -
```

The manifest includes:

- Dedicated `k8s-mcp-server` namespace
- ServiceAccount with a read-only ClusterRole covering all standard API groups
- Deployment with liveness/readiness probes on `/healthz`
- Hardened security context (non-root, read-only root filesystem, all capabilities dropped)
- Resource requests/limits (64Mi–256Mi memory, 50m–500m CPU)
- ClusterIP Service on port 8080

#### Automated Rebuild & Deploy

```bash
./redeploy.sh
```

This script builds the Docker image, pushes it, applies the Kubernetes manifests, syncs the kubeconfig secret, and triggers a rolling restart.

### MCP Client Configuration

For MCP clients that connect via streamable HTTP (e.g., in-cluster agents):

```
http://k8s-mcp-server.k8s-mcp-server.svc.cluster.local:8080/mcp
```

#### Cursor / VS Code

Add to your editor's MCP settings (`settings.json`):

```json
{
  "mcp.mcpServers": {
    "k8s-mcp-server": {
      "command": "k8s-mcp-server",
      "args": ["--mode", "stdio"],
      "env": {
        "KUBECONFIG": "${env:HOME}/.kube/config"
      }
    }
  }
}
```

For read-only mode, add `"--read-only"` to the `args` array.

## Security

### Container Hardening

The Docker image runs with a minimal attack surface:

- **Non-root user** (`appuser`, UID 1001)
- **Read-only root filesystem**
- **All capabilities dropped**
- **Minimal base image** (Alpine with only `ca-certificates`, `curl`, `tzdata`)
- **No privilege escalation** allowed

### Read-Only Mode

Use `--read-only` to guarantee no cluster state changes. This disables all 12 write tools (7 Kubernetes + 5 Helm) while keeping all 31 read-only tools available.

### RBAC

The included ClusterRole (`k8s-mcp-server-readonly`) grants read access to standard Kubernetes API groups:

- **Core** — pods, services, configmaps, secrets, namespaces, nodes, events, endpoints, PVs, PVCs, resource quotas, limit ranges, service accounts
- **apps** — deployments, replicasets, statefulsets, daemonsets
- **batch** — jobs, cronjobs
- **networking.k8s.io** — ingresses, network policies, ingress classes
- **storage.k8s.io** — storage classes, volume attachments
- **rbac.authorization.k8s.io** — roles, rolebindings, clusterroles, clusterrolebindings
- **autoscaling** — horizontal pod autoscalers
- **policy** — pod disruption budgets
- **metrics.k8s.io** — pod and node metrics

For write operations, create a separate ClusterRole with the necessary verbs and bind it to the ServiceAccount.

## Development

### Project Structure

```
├── .github/workflows/       # CI pipeline (build, test, vet, lint)
├── deploy/                  # Kubernetes deployment manifests
├── scripts/                 # Git hooks
├── src/
│   ├── main.go              # Entry point, tool registration
│   ├── Dockerfile           # Multi-stage build
│   ├── .golangci.yml        # Linter configuration
│   ├── tools/               # MCP tool schema definitions
│   ├── handlers/            # Tool request handlers
│   └── pkg/                 # Client libraries (k8s, helm)
├── docker-compose.yaml      # Local development
├── Makefile                 # Build, test, lint, format, deploy
└── redeploy.sh              # Build + deploy script
```

### Makefile Targets

| Target      | Description                                            |
|-------------|--------------------------------------------------------|
| `make build`      | Build the binary                                 |
| `make test`       | Run all unit tests                               |
| `make vet`        | Run `go vet` static analysis                     |
| `make fmt`        | Auto-format all Go source files with `gofmt`     |
| `make fmt-check`  | Check formatting without modifying (used in CI)  |
| `make lint`       | Run `golangci-lint` (skips if not installed)      |
| `make check`      | Run all checks: format, vet, test, build         |
| `make docker`     | Build the Docker image                           |
| `make deploy`     | Full rebuild + deploy via `redeploy.sh`          |
| `make clean`      | Remove build artifacts                           |
| `make hooks`      | Install the git pre-commit hook                  |

### Getting Started

```bash
# Install the pre-commit hook (runs fmt-check, vet, and tests before each commit)
make hooks

# Run the full check suite locally (same checks as CI)
make check
```

### Adding a New Tool

1. **Define the schema** in `tools/k8s.go` (or `tools/helm.go`):

```go
func MyNewTool() mcp.Tool {
    return mcp.NewTool("myNewTool",
        mcp.WithDescription("What this tool does"),
        mcp.WithString("param", mcp.Required(), mcp.Description("...")),
    )
}
```

2. **Implement the client method** in `pkg/k8s/client.go` (or `pkg/helm/client.go`)

3. **Create the handler** in `handlers/k8s.go` (or `handlers/helm.go`)

4. **Register** in `main.go`:

```go
s.AddTool(tools.MyNewTool(), handlers.MyNewHandler(client))
```

### Key Dependencies

| Package                       | Version | Purpose                     |
| ----------------------------- | ------- | --------------------------- |
| `github.com/mark3labs/mcp-go` | v0.41.1 | MCP protocol implementation |
| `helm.sh/helm/v3`             | v3.19.0 | Helm client library         |
| `k8s.io/client-go`            | v0.34.1 | Kubernetes client           |
| `k8s.io/metrics`              | v0.34.1 | Metrics API client          |

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
