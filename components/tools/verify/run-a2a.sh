#!/usr/bin/env bash
# Usage: run-a2a.sh AGENT "message text"
set -euo pipefail
D=$(cd "$(dirname "$0")" && pwd)
kubectl create configmap a2a-send -n kagent --from-file=a2a_send.py="$D/a2a_send.py" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl delete pod a2a-send -n kagent --ignore-not-found >/dev/null
kubectl apply -n kagent -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: a2a-send, namespace: kagent}
spec:
  restartPolicy: Never
  containers:
  - name: c
    image: python:3.12-alpine
    command: [python, /app/a2a_send.py, "$1", $(printf '%s' "$2" | jq -Rs .)]
    volumeMounts: [{name: app, mountPath: /app}]
    resources: {limits: {memory: 64Mi}}
  volumes: [{name: app, configMap: {name: a2a-send}}]
YAML
kubectl wait pod/a2a-send -n kagent --for=jsonpath='{.status.phase}'=Succeeded --timeout=200s >/dev/null || true
kubectl logs a2a-send -n kagent
kubectl delete pod a2a-send -n kagent --wait=false >/dev/null
