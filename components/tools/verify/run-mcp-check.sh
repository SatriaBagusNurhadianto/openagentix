#!/usr/bin/env bash
# Run verify/mcp_check.py from a one-off pod. Usage: run-mcp-check.sh [URL] [starlark-script]
set -euo pipefail
D=$(cd "$(dirname "$0")" && pwd)
URL=${1:-http://vmcp-tools.mcp-tools.svc.cluster.local:4483/mcp}
SCRIPT=${2:-}
kubectl create configmap mcp-check -n mcp-tools --from-file=mcp_check.py="$D/mcp_check.py" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl delete pod mcp-check -n mcp-tools --ignore-not-found >/dev/null
kubectl apply -n mcp-tools -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: mcp-check, namespace: mcp-tools}
spec:
  restartPolicy: Never
  containers:
  - name: c
    image: python:3.12-alpine
    command: [python, /app/mcp_check.py, "$URL"]
    $( [ -n "$SCRIPT" ] && printf 'args: [%s]' "$(printf '%s' "$SCRIPT" | jq -Rs .)" )
    volumeMounts: [{name: app, mountPath: /app}]
    resources: {limits: {memory: 64Mi}}
  volumes: [{name: app, configMap: {name: mcp-check}}]
YAML
kubectl wait pod/mcp-check -n mcp-tools --for=jsonpath='{.status.phase}'=Succeeded --timeout=120s >/dev/null || true
kubectl logs mcp-check -n mcp-tools
kubectl delete pod mcp-check -n mcp-tools --wait=false >/dev/null
