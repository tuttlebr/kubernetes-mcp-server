# Repository Guidelines

## Project Structure & Module Organization

This is a Go MCP server for Kubernetes and Helm. The Go module is rooted in `src/`, so run raw Go commands there or use the root `Makefile`.

- `src/main.go`: server flags, transport setup, and tool registration.
- `src/tools/`: MCP tool schema definitions.
- `src/handlers/`: request parsing, client calls, responses.
- `src/pkg/k8s`, `src/pkg/helm`, `src/pkg/agent`: implementation packages.
- `*_test.go`: unit tests beside the package under test.
- `deploy/`, `docker-compose.yaml`, `deploy.sh`: deployment and local runtime assets. `redeploy.sh` is a compatibility wrapper.
- `mcp-functions.yaml`, `mcp-tool-names.yaml`, `src/mcp-config.json`: MCP metadata.

`src/skills/*/` is gitignored for user-local OpenCode agent skills.

## Build, Test, and Development Commands

Prefer Make targets because they enter `src/`, set `CGO_ENABLED=0`, and use `-buildvcs=false`.

- `make build`: compile `src/k8s-mcp-server`.
- `make test`: run `go test ./...`.
- `make vet`: run `go vet ./...`.
- `make fmt`: apply `gofmt -w .`.
- `make fmt-check`: check formatting without edits.
- `make lint`: run `golangci-lint` when installed.
- `make check`: run format, vet, tests, and build.
- `make docker`: build with Docker Compose.
- `make hooks`: install the pre-commit hook.

For a focused test, use `cd src && go test ./pkg/k8s -run TestTruncateJSON -v`.

## Coding Style & Naming Conventions

Use standard Go formatting with tabs via `gofmt`. Keep tool schemas in `tools/`, handlers in `handlers/`, and cluster or Helm logic in `pkg/`. Public identifiers use `PascalCase`; unexported helpers use `camelCase`. MCP tool names use lower camel case, such as `devopsAgent`.

New Kubernetes handlers should use the existing safe marshaling path so large responses and secrets are handled consistently.

## Testing Guidelines

Tests use Go's standard `testing` package. Name files `*_test.go` and tests `TestXxx`. Place tests beside the package they exercise, such as `src/pkg/k8s/sanitize_test.go`. Run `make test` before committing and `make check` before a PR.

## Commit & Pull Request Guidelines

Recent history uses short, imperative subjects, for example `Fix opencode MCP agent runtime wiring` and `expand helm support`. Keep each commit focused.

PRs should include a concise description, linked issue when applicable, config or security implications, and checks run locally. CI runs formatting, vet, tests, build, `golangci-lint`, Docker build, Trivy, and `govulncheck`.

## Security & Configuration Tips

Do not commit `.env`, kubeconfigs, generated caches, or local skills. Be careful with write-capable Kubernetes and Helm tools; preserve `--read-only`, `--no-k8s`, `--no-helm`, and `--no-agent` gating when adding tools.
