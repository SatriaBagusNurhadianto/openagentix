#!/usr/bin/env bash
# Idempotent installer for the OpenAgentix tool layer (ToolHive + vMCP code mode + kagent wiring).
# Prereqs: kind cluster with kagent+substrate (components/runtime/up.sh), kubectl, helm. Context defaults to kind-openagentix.
# Optional: WITH_SKILL=1 builds/pushes the example OCI skill (needs docker + the ax-registry container).
set -euo pipefail
cd "$(dirname "$0")"
CTX=${KUBE_CONTEXT:-kind-openagentix}
kubectl config use-context "$CTX" >/dev/null
TOOLHIVE_VERSION=0.67.1

helm upgrade --install toolhive-operator-crds oci://ghcr.io/stacklok/toolhive/toolhive-operator-crds \
  --version "$TOOLHIVE_VERSION" -n toolhive-system --create-namespace --wait
helm upgrade --install toolhive-operator oci://ghcr.io/stacklok/toolhive/toolhive-operator \
  --version "$TOOLHIVE_VERSION" -n toolhive-system --wait --timeout 5m

# PREREQS_ONLY=1 (used by ../../install.sh): stop after the operator; charts/openagentix owns the CRs.
[ "${PREREQS_ONLY:-0}" = 1 ] && { echo "ToolHive $TOOLHIVE_VERSION installed (PREREQS_ONLY=1)"; exit 0; }

kubectl apply -f manifests/10-mcp-layer.yaml
kubectl wait virtualmcpserver/tools -n mcp-tools --for=jsonpath='{.status.phase}'=Ready --timeout=300s

kubectl apply -f manifests/20-remote-mcp.yaml
kubectl wait remotemcpserver/toolhive-vmcp -n kagent --for=condition=Accepted --timeout=60s

kubectl create configmap mock-llm-tools -n kagent --from-file=server.py=stub/mock_llm_tools.py \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f manifests/30-mock-llm-tools.yaml
kubectl rollout status deploy/mock-llm-tools -n kagent --timeout=120s
kubectl apply -f manifests/40-tools-agent.yaml
kubectl wait agent/tools-agent -n kagent --for=condition=Ready --timeout=300s

if [ "${WITH_SKILL:-0}" = 1 ]; then
  REG=172.18.0.2:5000   # kind node IP; ax-registry shares the node netns
  T=$(mktemp -d)
  docker build -q --platform linux/arm64 -t openagentix-canary-skill:1.0.0 -f skills/Dockerfile skills
  docker save -o "$T/skill.tar" openagentix-canary-skill:1.0.0
  DIGEST=$(docker run --rm --network container:openagentix-control-plane -v "$T:/w" \
    gcr.io/go-containerregistry/crane:latest push /w/skill.tar "$REG/skills/openagentix-canary:1.0.0" --insecure 2>&1 \
    | rg -o 'sha256:[0-9a-f]{64}' | tail -1)
  sed -E "s#(oci: ).*#\1$REG/skills/openagentix-canary@$DIGEST#" manifests/50-skill-agent.yaml | kubectl apply -f -
  kubectl wait agent/tools-skill-agent -n kagent --for=condition=Ready --timeout=300s
fi
echo "done. Verify: verify/run-mcp-check.sh ; verify/run-a2a.sh tools-agent 'use tool ...'"
