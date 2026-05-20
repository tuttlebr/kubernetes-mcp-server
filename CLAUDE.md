# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Layout

This is a Go module rooted at `src/` (not at the repo root). All Go commands must be run from `src/`, and the `Makefile` at the repo root takes care of that for you. The repo root holds Kubernetes manifests (`deploy/`), Docker Compose config, and the redeploy script.

## Build, Test, Lint

Always prefer `make` targets — they `cd` into `src/`, set `CGO_ENABLED=0`, and pass `-buildvcs=false` consistently.

```bash
make build       # CGO_ENABLED=0 go build -ldflags="-w -s" -o k8s-mcp-server .
make test        # go test ./...
make vet         # go vet ./...
make fmt         # gofmt -w .
make fmt-check   # CI-style format check; fails if any file is unformatted
make lint        # golangci-lint run (skips silently if not installed)
make check       # fmt-check + vet + test + build (matches CI)
make docker      # docker compose build
make deploy      # ./deploy.sh — full image build/push + apply + rollout restart
make hooks       # install scripts/pre-commit (runs fmt-check + vet + test before each commit)
```

Run a single test from `src/`:

```bash
cd src && go test ./handlers/ -run TestGetStringArg -v
cd src && go test ./pkg/k8s/ -run TestTruncateJSON -v
```

CI (`.github/workflows/ci.yml`) runs `make fmt-check`, `make vet`, `make test`, `make build`, then `golangci-lint` (v2.1.6), a Docker build, Trivy filesystem scan, and `govulncheck`. The pre-commit hook mirrors the first three steps locally — installing it via `make hooks` is the easiest way to keep parity with CI.

## Architecture

The server is an MCP (Model Context Protocol) server that exposes Kubernetes and Helm operations as tools. Every tool follows a strict 4-file pattern:

```
src/
├── main.go                  # Flags, tool registration, transport setup
├── tools/{k8s,helm,agent}.go     # mcp.Tool schema definitions (no logic)
├── handlers/{k8s,helm,agent}.go  # Argument parsing → client call → response
└── pkg/
    ├── k8s/client.go        # Kubernetes client (dynamic + discovery + metrics)
    ├── k8s/sanitize.go      # Response truncation + secret redaction
    ├── k8s/gpu.go           # GPU-specific inspection helpers
    ├── helm/client.go       # Helm client (install, upgrade, releases)
    └── agent/agent.go       # OpenCode subprocess orchestration
```

To add a tool: define the schema in `tools/`, implement the underlying method on the client in `pkg/`, write the request/response handler in `handlers/`, then register it in `main.go` (inside the appropriate `--no-k8s`/`--no-helm` and `--read-only` gates).

### Dynamic GVR resolution

`pkg/k8s/client.go` does not hardcode resource type → API group mappings. It uses a `dynamicClient` with a cached `GroupVersionResource` resolver (`getCachedGVR`) backed by the discovery API. The cache is keyed by PascalCase Kind, lowercase plural, lowercase singular, and short names simultaneously — so `Deployment`, `deployments`, `deployment`, and `deploy` all resolve to the same GVR. TTL is 5 minutes (`gvrCacheTTL`); the cache rebuilds on miss with a double-checked write lock to avoid thundering herds. This is what makes the server work transparently with CRDs.

### Response sanitization & size cap

Every handler returns through `marshalSafe()` (in `handlers/k8s.go`), which calls `k8s.TruncateJSON`. Responses larger than 128 KB (`maxResponseBytes` in `pkg/k8s/sanitize.go`) are replaced with a structured `{"error":"response too large", ...}` payload rather than truncated mid-stream. `pkg/k8s/SanitizeResource` separately redacts Secret `data`/`stringData`, replaces ConfigMap `binaryData` with size placeholders, strips `managedFields` and `last-applied-configuration`, and replaces large base64-looking blobs anywhere in the tree. New handlers MUST go through `marshalSafe` so these protections apply uniformly.

### Read-only gating

The `--read-only` flag does not check at request time — it controls whether write tools are registered at all in `main.go`. Adding a new write tool means putting its `s.AddTool(...)` call inside the `if !readOnly { ... }` block. `--no-k8s` and `--no-helm` are mutually exclusive (enforced at startup) and disable their entire tool category. `--no-agent`, `OPENCODE_BASE_URL` unset, or missing opencode CLI all suppress registration of the `devopsAgent` tool gracefully.

### DevOps agent (recursive MCP)

`pkg/agent/agent.go` implements an interesting pattern: the `devopsAgent` tool spawns `opencode run` as a subprocess, and writes an opencode config that points opencode at a **child instance of this same binary** running with `--mode stdio --no-agent`. The child handles MCP tool calls; the parent surfaces the agent's NDJSON output back to the original MCP client. When the parent runs with `--read-only`, the handler forces `--read-only` on the child invocation regardless of the `readOnly` parameter (`handlers/agent.go:27` + `pkg/agent/agent.go:159`). `c.config.BinaryPath` is resolved via `os.Executable()` + `filepath.EvalSymlinks` at startup, so the child invocation works from any install path.

### Transport modes

`--mode` selects `stdio` (CLI/editor integrations), `sse` (default per Dockerfile-set env, kept for legacy clients), or `streamable-http` (preferred for HTTP clients; endpoint `/mcp`). Both HTTP modes wrap the MCP handler with `loggingMiddleware` and serve `GET /healthz` directly — the health check calls `client.CheckConnection()` against the API server, returning 200/503. Graceful shutdown listens on SIGINT/SIGTERM with a 10s timeout.

## Kubeconfig Resolution

`k8s.NewClient("")` resolves config in this order: explicit path → `KUBECONFIG` env var → in-cluster ServiceAccount → `~/.kube/config`. The resolved path is exposed via `client.KubeconfigPath()` and passed to the agent so child subprocesses see the same config.

## Skills

OpenCode skills live under `src/skills/<name>/SKILL.md` and are copied into the Docker image at `/home/appuser/.config/opencode/skills/` during build (Dockerfile stage 3). Skills can be hot-mounted by volume-mounting over that path. Per `.gitignore`, `src/skills/*/` subdirectories are ignored — only the directory scaffolding is tracked; concrete skills are user-local.

## Docker

The Dockerfile is a three-stage build: Go binary → opencode (via Bun) → Alpine runtime with helm v3.17.3 and kubectl v1.34.1 installed (both with SHA256 verification). The runtime image runs as UID 1001 (`appuser`), drops all capabilities, and uses a read-only root filesystem when deployed via `deploy/k8s-mcp-server.yaml`. `deploy.sh` builds + pushes the image, applies the manifest, syncs the kubeconfig secret (and optionally the agent-config secret from `.env`), then restarts the DaemonSet.

## Key Dependencies

| Package                       | Version  | Purpose                     |
| ----------------------------- | -------- | --------------------------- |
| `github.com/mark3labs/mcp-go` | v0.41.1  | MCP protocol implementation |
| `k8s.io/client-go`            | v0.34.1  | Dynamic + typed K8s clients |
| `k8s.io/metrics`              | v0.34.1  | Metrics API client          |
| `helm.sh/helm/v3`             | v3.19.0  | Helm client library         |

Go toolchain: `go 1.24.1` (with `toolchain go1.24.6`). `GOTOOLCHAIN=auto` is set in the Docker builder.
