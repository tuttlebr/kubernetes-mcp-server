#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

load_env_file() {
  local file="$1"
  if [[ -f "${file}" ]]; then
    echo "Loading ${file#"${WORKSPACE_DIR}/"}"
    set -a
    # shellcheck source=/dev/null
    source "${file}"
    set +a
  fi
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

is_true() {
  case "${1:-}" in
    true|TRUE|1|yes|YES) return 0 ;;
    *) return 1 ;;
  esac
}

load_env_file "${WORKSPACE_DIR}/.env"
load_env_file "${SCRIPT_DIR}/.env"

NAMESPACE="k8s-mcp-server"
MANIFEST="${K8S_MCP_MANIFEST:-${SCRIPT_DIR}/deploy/k8s-mcp-server.yaml}"
BUILD_IMAGE="${BUILD_IMAGE:-true}"
PUSH_IMAGE="${PUSH_IMAGE:-true}"
DRY_RUN="${DRY_RUN:-false}"

require_command kubectl
if ! is_true "${DRY_RUN}" && { is_true "${BUILD_IMAGE}" || is_true "${PUSH_IMAGE}"; }; then
  require_command docker
fi

cd "${SCRIPT_DIR}"

if [[ -z "${MCP_AUTH_TOKEN:-}" ]]; then
  cat >&2 <<EOF
MCP_AUTH_TOKEN is required for the default authenticated Kubernetes deployment.
Set it in ${WORKSPACE_DIR}/.env, ${SCRIPT_DIR}/.env, or the current environment.
EOF
  exit 1
fi

if ! is_true "${DRY_RUN}" && is_true "${BUILD_IMAGE}"; then
  docker compose build
fi

if ! is_true "${DRY_RUN}" && is_true "${PUSH_IMAGE}"; then
  docker compose push
fi

if is_true "${DRY_RUN}"; then
  kubectl apply --dry-run=client -f "${MANIFEST}"
  kubectl -n "${NAMESPACE}" create secret generic k8s-mcp-auth \
    --from-literal=token="${MCP_AUTH_TOKEN}" \
    --dry-run=client -o yaml >/dev/null
  exit 0
fi

kubectl apply -f "${MANIFEST}"

echo "Syncing MCP auth secret..."
kubectl -n "${NAMESPACE}" create secret generic k8s-mcp-auth \
  --from-literal=token="${MCP_AUTH_TOKEN}" \
  --dry-run=client -o yaml | kubectl apply -f -

if [[ -n "${OPENCODE_BASE_URL:-}" || -n "${OPENCODE_API_KEY:-}" || -n "${OPENCODE_MODEL:-}" ]]; then
  echo "Syncing agent-config secret..."
  kubectl -n "${NAMESPACE}" create secret generic k8s-mcp-agent-config \
    --from-literal=base-url="${OPENCODE_BASE_URL:-}" \
    --from-literal=api-key="${OPENCODE_API_KEY:-}" \
    --from-literal=model="${OPENCODE_MODEL:-}" \
    --dry-run=client -o yaml | kubectl apply -f -
else
  echo "Skipping agent-config secret (no OPENCODE_* vars set)."
fi

kubectl -n "${NAMESPACE}" rollout restart daemonset/k8s-mcp-server
kubectl -n "${NAMESPACE}" rollout status daemonset/k8s-mcp-server
kubectl -n "${NAMESPACE}" get all -o wide
