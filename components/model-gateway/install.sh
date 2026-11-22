#!/usr/bin/env bash
# Idempotent install of agentgateway (model gateway) on the kind-openagentix cluster.
# Secrets come from the environment only and are never echoed or written to disk:
#   GOOGLE_API_KEY  -> Secret agentgateway-system/gemini-api-key   (optional: without it the `gemini` model returns 401/400)
# Virtual keys are generated with openssl on first run and kept in cluster Secrets; only SHA-256 hashes go in the ConfigMap.
set -euo pipefail
cd "$(dirname "$0")"

GWAPI_VERSION=1.6.3          # Gateway API standard channel (docs: 1.6.3)
AGW_VERSION=v1.7.0           # latest stable at 2026-11-22 (GitHub releases: v1.7.0; rc/alpha ignored)
AGW_OCI=oci://cr.agentgateway.dev/charts
NS=agentgateway-system
K=${KUBECTL:-kubectl}        # `kubectl` may be wrapped by a shell hook; override with KUBECTL=/path/to/real/kubectl
CTX=${KUBE_CONTEXT:-kind-openagentix}
$K config use-context "$CTX" >/dev/null
log(){ printf '\n=== %s\n' "$*"; }

# --- 0. collision checks (Substrate's atenet-router runs the agentgateway *image* but owns no CRD/GatewayClass) ----
log "collision check"
for crd in $($K get crd -o name 2>/dev/null | sed 's|.*/||' | grep -E 'gateway.networking.k8s.io$|agentgateway.dev$' || true); do echo "existing CRD: $crd"; done
if $K get gatewayclass agentgateway >/dev/null 2>&1; then
  ctrl=$($K get gatewayclass agentgateway -o jsonpath='{.spec.controllerName}')
  [ "$ctrl" = "agentgateway.dev/agentgateway" ] || { echo "STOP: GatewayClass 'agentgateway' is owned by '$ctrl'"; exit 1; }
fi
if $K get crd gateways.gateway.networking.k8s.io >/dev/null 2>&1; then
  have=$($K get crd gateways.gateway.networking.k8s.io -o jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}')
  echo "Gateway API CRDs already present (bundle $have); not touching them unless FORCE_GATEWAY_API=1"
fi

# --- 1. Gateway API CRDs ------------------------------------------------------------------------------------------
log "Gateway API CRDs v$GWAPI_VERSION (standard)"
if [ "${FORCE_GATEWAY_API:-0}" = 1 ] || ! $K get crd gateways.gateway.networking.k8s.io >/dev/null 2>&1; then
  $K apply --server-side --force-conflicts -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/v$GWAPI_VERSION/standard-install.yaml"
fi

# --- 2. agentgateway CRDs + control plane --------------------------------------------------------------------------
log "agentgateway $AGW_VERSION"
helm upgrade --install agentgateway-crds $AGW_OCI/agentgateway-crds --version $AGW_VERSION -n $NS --create-namespace --wait
helm upgrade --install agentgateway $AGW_OCI/agentgateway --version $AGW_VERSION -n $NS -f values.yaml --wait --timeout 3m

[ "${PREREQS_ONLY:-0}" = 1 ] && { echo "agentgateway $AGW_VERSION installed (PREREQS_ONLY=1)"; exit 0; }

# --- 3. secrets (env only) -----------------------------------------------------------------------------------------
log "secrets"
if [ -n "${GOOGLE_API_KEY:-}" ]; then
  # process substitution: the key never appears in argv or on disk. Secret key name must be 'Authorization'.
  $K create secret generic gemini-api-key -n $NS --from-file=Authorization=<(printf '%s' "$GOOGLE_API_KEY") \
    --dry-run=client -o yaml | $K apply -f - >/dev/null
  echo "gemini-api-key applied"
else
  echo "WARN: GOOGLE_API_KEY not set; gemini-api-key not created (gemini/auto-primary will fail until it is)"
fi
$K get ns kagent >/dev/null 2>&1 && KAGENT=1 || KAGENT=0
mkvk(){ # mkvk <ns> <secret> <key>: create virtual key once, never print it
  $K get secret -n "$1" "$2" >/dev/null 2>&1 || \
    $K create secret generic "$2" -n "$1" --from-file="$3"=<(printf 'vk-%s' "$(openssl rand -hex 16)") >/dev/null
}
hash(){ printf '%s' "$($K get secret -n "$1" "$2" -o jsonpath="{.data.$3}" | base64 -d)" | shasum -a 256 | awk '{print "sha256:"$1}'; }
mkvk $NS ops-test-virtual-key key
CM_ARGS=(--from-literal="ops-test={\"keyHash\":\"$(hash $NS ops-test-virtual-key key)\",\"metadata\":{\"user_id\":\"ops-test\"}}")
if [ $KAGENT = 1 ]; then
  mkvk kagent gateway-virtual-key OPENAI_API_KEY
  CM_ARGS+=(--from-literal="gateway-agent={\"keyHash\":\"$(hash kagent gateway-virtual-key OPENAI_API_KEY)\",\"metadata\":{\"user_id\":\"gateway-agent\"}}")
fi
$K create configmap llm-api-keys -n $NS "${CM_ARGS[@]}" --dry-run=client -o yaml \
  | $K label --local -f - agentgateway.dev/apikey=true -o yaml | $K apply -f - >/dev/null
echo "virtual keys: ConfigMap llm-api-keys ($(( ${#CM_ARGS[@]} )) hashed keys)"

# --- 4. gateway, models, policies ----------------------------------------------------------------------------------
log "manifests"
$K apply -f manifests/00-gateway.yaml
$K wait gateway/model-gateway -n $NS --for=condition=Programmed --timeout=120s
$K apply -f manifests/10-models.yaml -f manifests/20-policies.yaml -f manifests/30-observability.yaml
if [ $KAGENT = 1 ]; then $K apply -f manifests/40-kagent.yaml; else echo "kagent namespace absent: skipped manifests/40-kagent.yaml"; fi

log "status"
$K get gateway,agentgatewaymodel,agentgatewaypolicy,pods -n $NS
echo "endpoint (in-cluster): http://model-gateway.$NS.svc.cluster.local/v1"
