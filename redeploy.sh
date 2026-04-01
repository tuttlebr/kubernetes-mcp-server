#!/bin/bash
set -e
clear

KUBECONFIG_SRC="${KUBECONFIG:-$HOME/.kube/config}"
NAMESPACE="k8s-mcp-server"

# Source .env if present
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "$SCRIPT_DIR/.env" ]]; then
  echo "Loading .env..."
  set -a
  # shellcheck source=/dev/null
  source "$SCRIPT_DIR/.env"
  set +a
fi

docker compose build
docker compose push

kubectl apply -f deploy/k8s-mcp-server.yaml

# --- kubeconfig secret ---
echo "Syncing kubeconfig secret from $KUBECONFIG_SRC..."
kubectl -n "$NAMESPACE" create secret generic k8s-mcp-kubeconfig \
  --from-file=config="$KUBECONFIG_SRC" \
  --dry-run=client -o yaml | kubectl apply -f -

# --- agent-config secret (from .env, optional) ---
if [[ -n "${OPENCODE_BASE_URL:-}" || -n "${OPENCODE_API_KEY:-}" || -n "${OPENCODE_MODEL:-}" ]]; then
  echo "Syncing agent-config secret..."
  kubectl -n "$NAMESPACE" create secret generic k8s-mcp-agent-config \
    --from-literal=base-url="${OPENCODE_BASE_URL:-}" \
    --from-literal=api-key="${OPENCODE_API_KEY:-}" \
    --from-literal=model="${OPENCODE_MODEL:-}" \
    --dry-run=client -o yaml | kubectl apply -f -
else
  echo "Skipping agent-config secret (no OPENCODE_* vars set)."
fi

kubectl -n "$NAMESPACE" rollout restart daemonset/k8s-mcp-server
kubectl -n "$NAMESPACE" rollout status daemonset/k8s-mcp-server
sleep 20
kubectl -n "$NAMESPACE" get all -o wide
