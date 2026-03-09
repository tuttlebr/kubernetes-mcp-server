#!/bin/bash
set -e
clear

KUBECONFIG_SRC="${KUBECONFIG:-$HOME/.kube/config}"
NAMESPACE="k8s-mcp-server"
SECRET_NAME="k8s-mcp-kubeconfig"

docker compose build
docker compose push

kubectl apply -f deploy/k8s-mcp-server.yaml

echo "Syncing kubeconfig from $KUBECONFIG_SRC into secret/$SECRET_NAME..."
kubectl -n "$NAMESPACE" create secret generic "$SECRET_NAME" \
  --from-file=config="$KUBECONFIG_SRC" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n "$NAMESPACE" rollout restart deployment/k8s-mcp-server
kubectl -n "$NAMESPACE" rollout status deployment/k8s-mcp-server --timeout=60s
sleep 20
kubectl -n "$NAMESPACE" get all -o wide
