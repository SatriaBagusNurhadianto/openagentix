#!/usr/bin/env bash
# Agent Kourier as its own Helm release (ns agent-kourier), pinned and signature-verified. Called by ../../install.sh and
# ../../uninstall.sh; usable on its own.
#   kourier.sh install     needs KOURIER_TEAM_NS (namespaces whose connections the broker serves, space separated)
#                          optional KOURIER_EGRESS (JSON list of NetworkPolicy egress rules, from the chart's marker ConfigMap)
#                          optional KOURIER_VALUES_FILE (extra values files, space separated, applied last)
#                          optional KOURIER_APISERVER_CIDRS (CIDRs, comma or space separated, the API server addresses the NetworkPolicy
#                          allows; wins over detection. Default: on kind the /24 of the API server's node address, so that an address
#                          change after a Docker restart does not cut Kourier off; elsewhere the exact endpoint addresses (/32).
#                          See apiserver-cidrs.sh; offline tests: tests/run.sh)
#   kourier.sh uninstall   removes the release; PURGE_DATA=1 also its PVC and the three CRDs (this deletes every
#                          ChatConnection/Binding/AgentBackend that is left); the namespace is kept
# Idempotent: when the release already has this chart version and exactly these values, nothing is changed (no new
# revision, no pod restart). Never prints a secret value (there are none here).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=apiserver-cidrs.sh
. "$HERE/apiserver-cidrs.sh"
CTX=${KUBE_CONTEXT:-kind-${CLUSTER:-openagentix}}
K="${KUBECTL:-kubectl} --context $CTX"
REL=agent-kourier
NS=${KOURIER_NS:-agent-kourier}
VERSION=0.6.0
CHART=oci://ghcr.io/agentkourier/charts/agent-kourier
IDENTITY="https://github.com/agentkourier/agent-kourier/.github/workflows/release.yml@refs/tags/v$VERSION"
ISSUER=https://token.actions.githubusercontent.com
CRDS="agentbackends.agentkourier.dev bindings.agentkourier.dev chatconnections.agentkourier.dev"

case "${1:-}" in
install)
  T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
  # desired release values: the file in this directory, the namespaces and egress from the chart, the API server address
  # which API server addresses the NetworkPolicy allows: see apiserver-cidrs.sh (kind: the node /24; elsewhere: the exact addresses)
  EPS=""; NODES=""
  if [ -z "$(printf '%s' "${KOURIER_APISERVER_CIDRS:-}" | tr -d '[:space:],')" ]; then
    EPS=$($K get endpointslices.discovery.k8s.io -n default -l kubernetes.io/service-name=kubernetes -o json)
    NODES=$($K get nodes -o json 2>/dev/null || echo '{"items":[]}')   # only used to recognise kind; unreadable means "not kind": exact addresses
  fi
  APISERVER=$(kourier_apiserver_cidrs "${KOURIER_APISERVER_CIDRS:-}" "$EPS" "$NODES") || exit 1
  echo "agent-kourier: the NetworkPolicy lets it reach the API server at $APISERVER"
  NSJSON=$(printf '%s\n' ${KOURIER_TEAM_NS:?KOURIER_TEAM_NS is required} | jq -R . | jq -sc .)
  yq -c . "$HERE/kourier-values.yaml" > "$T/base.json"
  jq -n --argjson ns "$NSJSON" --argjson api "$APISERVER" --argjson egress "${KOURIER_EGRESS:-[]}" \
    '{config:{connectionNamespaces:$ns}, networkPolicy:{kubeApiServer:{cidrs:$api}, extraEgress:$egress}}' > "$T/derived.json"
  FILES=("$T/base.json" "$T/derived.json"); for f in ${KOURIER_VALUES_FILE:-}; do yq -c . "$f" > "$T/extra-$(basename "$f").json"; FILES+=("$T/extra-$(basename "$f").json"); done
  jq -s 'def m(a;b): a as $a|b as $b|if ($a|type)=="object" and ($b|type)=="object" then reduce ($a+$b|keys_unsorted[]) as $k ({}; .[$k]=(if ($b|has($k)) and ($a|has($k)) then m($a[$k];$b[$k]) elif ($b|has($k)) then $b[$k] else $a[$k] end)) else $b end; reduce .[] as $x ({}; m(.;$x))' "${FILES[@]}" > "$T/values.json"
  HAVE_VER=$(helm --kube-context "$CTX" list -n "$NS" -f "^$REL\$" -o json 2>/dev/null | jq -r '.[0].chart // ""')
  if [ "$HAVE_VER" = "agent-kourier-$VERSION" ] \
     && [ "$(helm --kube-context "$CTX" get values $REL -n "$NS" -o json | jq -S .)" = "$(jq -S . "$T/values.json")" ] \
     && $K get crd $CRDS >/dev/null 2>&1; then
    echo "agent-kourier $VERSION: release already installed with these values, nothing to do"
    exit 0
  fi
  if command -v cosign >/dev/null; then
    CD=$(helm pull "$CHART" --version $VERSION --destination "$T" 2>&1 | sed -n 's/^Digest: //p')
    IMG=$(helm show values "$CHART" --version $VERSION | sed -n 's/^  digest: //p' | head -1)
    [ -n "$CD" ] && [ -n "$IMG" ] || { echo "ERROR: could not read the chart digest or the image digest to verify" >&2; exit 1; }
    cosign verify "ghcr.io/agentkourier/charts/agent-kourier@$CD" --certificate-oidc-issuer $ISSUER --certificate-identity "$IDENTITY" >/dev/null 2>&1 \
      || { echo "ERROR: cosign could not verify the chart $CD against $IDENTITY" >&2; exit 1; }
    cosign verify "ghcr.io/agentkourier/agent-kourier@$IMG" --certificate-oidc-issuer $ISSUER --certificate-identity "$IDENTITY" >/dev/null 2>&1 \
      || { echo "ERROR: cosign could not verify the image $IMG against $IDENTITY" >&2; exit 1; }
    echo "cosign: chart $CD and image $IMG verified (identity release.yml@v$VERSION)"
  else
    echo "WARN: cosign is not installed: the chart and image signatures of agent-kourier $VERSION were NOT verified (install cosign to verify)"
  fi
  helm --kube-context "$CTX" upgrade --install $REL "$CHART" --version $VERSION -n "$NS" --create-namespace -f "$T/values.json" --wait --timeout 5m
  ;;
uninstall)
  if helm --kube-context "$CTX" status $REL -n "$NS" >/dev/null 2>&1; then helm --kube-context "$CTX" uninstall $REL -n "$NS" --wait; else echo "release $REL not found in $NS"; fi
  if [ "${PURGE_DATA:-0}" = 1 ]; then
    $K delete pvc --all -n "$NS" --ignore-not-found
    $K delete crd $CRDS --ignore-not-found
  fi
  ;;
*) echo "usage: $0 install|uninstall" >&2; exit 2;;
esac
