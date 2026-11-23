#!/usr/bin/env bash
# Removes ONLY the openagentix Helm release (bridge, gateway config, MCP layer, kagent wiring and agents).
# Does not touch kind, kagent, Substrate, agentgateway, ToolHive, namespaces, or any Secret/ConfigMap
# (provider keys, virtual keys, llm-api-keys, telegram-bridge), so a later ./install.sh reuses them.
# PURGE_SECRETS=1 additionally deletes the Secrets/ConfigMap that install.sh created for this release (except the
# knowledge base database credentials: they must stay as long as its volume does).
# PURGE_SECRETS=1 also deletes the notify service's Secrets (the destinations file with the chat ids, and the copied bot token).
# Observability: the release's objects (bundled VictoriaMetrics and Jaeger, monitors, telemetry configs) go with the release; PURGE_DATA=1 also
# deletes the VictoriaMetrics volume. The kagent controller's telemetry settings and Agent Kourier's OTLP env are NOT reverted by uninstalling
# (they exporter-fail quietly); re-run ./install.sh WITHOUT the observability values to remove them.
# PURGE_DATA=1 deletes the knowledge base volumes (database, model cache), the memory embedding model cache and the database credentials.
# Agent Kourier (installed by install.sh when channels.kourier.enabled): its Helm release is removed too (after the openagentix
# release, which owns the connections and bindings). PURGE_SECRETS=1 also deletes the webhook connection Secrets install.sh generated
# (Slack Secrets are yours and are never deleted). PURGE_DATA=1 also deletes Kourier's PVC (its SQLite state) and its three CRDs, which
# the chart keeps on uninstall (deleting them deletes any remaining ChatConnection/Binding/AgentBackend). Left in place always:
# the namespaces agent-kourier and openagentix-channels and, without the purge flags, the Secrets and the PVC.
set -euo pipefail
CLUSTER=${CLUSTER:-openagentix}; CTX=${KUBE_CONTEXT:-kind-$CLUSTER}
K=${KUBECTL:-kubectl}; RELEASE_NS=${RELEASE_NS:-openagentix-system}
$K config use-context "$CTX" >/dev/null || { echo "ERROR: kube context $CTX not found" >&2; exit 1; }
VKEYS=""
if [ "${PURGE_SECRETS:-0}" = 1 ]; then   # read per-agent virtual key secret names before the release is removed
  VKEYS=$(helm get manifest openagentix -n "$RELEASE_NS" 2>/dev/null | yq -r 'select(.kind=="ModelConfig")|.metadata.namespace+"/"+.spec.apiKeySecret' 2>/dev/null || true)
fi
NOTIFYSECRETS=$(helm get manifest openagentix -n "$RELEASE_NS" 2>/dev/null | yq -r 'select(.kind=="Deployment" and .metadata.name=="notify")|.metadata.namespace as $ns|([.spec.template.spec.volumes[]?.secret.secretName]+[.spec.template.spec.containers[0].env[]?.valueFrom.secretKeyRef.name])|map(select(.!=null))|unique|.[]|$ns+"/"+.' 2>/dev/null || true)
WHSECRETS=$(helm get manifest openagentix -n "$RELEASE_NS" 2>/dev/null | yq -r 'select(.kind=="ChatConnection" and .spec.platform=="webhook")|.metadata.namespace+"/"+.spec.credentialsSecretRef.name' 2>/dev/null || true)
if helm status openagentix -n "$RELEASE_NS" >/dev/null 2>&1; then
  helm uninstall openagentix -n "$RELEASE_NS" --wait
else
  echo "release openagentix not found in $RELEASE_NS; nothing to do"
fi
KOURIER_NS=${KOURIER_NS:-agent-kourier} CLUSTER="$CLUSTER" KUBE_CONTEXT="$CTX" KUBECTL="$K" bash "$(dirname "$0")/components/channels/kourier.sh" uninstall
if [ "${PURGE_SECRETS:-0}" = 1 ]; then
  for row in $WHSECRETS; do $K delete secret -n "${row%%/*}" "${row##*/}" --ignore-not-found; done
  for row in $VKEYS; do $K delete secret -n "${row%%/*}" "${row##*/}" --ignore-not-found; done
  $K delete configmap llm-api-keys -n agentgateway-system --ignore-not-found
  $K delete secret kb-admin kb-virtual-key -n openagentix-knowledge --ignore-not-found
  for row in $NOTIFYSECRETS; do $K delete secret -n "${row%%/*}" "${row##*/}" --ignore-not-found; done   # notify destinations (chat ids) and the copied bot token
  $K delete secret telegram-bridge -n openagentix-gateway --ignore-not-found
  $K delete secret anthropic-api-key gemini-api-key -n agentgateway-system --ignore-not-found
fi
if [ "${PURGE_DATA:-0}" = 1 ]; then   # the knowledge base's data: database volume, its credentials, the model cache
  $K delete pvc data-kb-postgres-0 kb-embeddings-models -n openagentix-knowledge --ignore-not-found
  $K delete secret kb-postgres kb-database -n openagentix-knowledge --ignore-not-found
  $K delete pvc victoria-metrics -n openagentix-observability --ignore-not-found   # bundled metrics store
  $K delete pvc memory-embeddings-models -n openagentix-memory --ignore-not-found   # memory embedding model cache (saved memories live in kagent's PostgreSQL)
fi
echo "removed release openagentix. Upstream components and namespaces were left in place."
