#!/usr/bin/env bash
# Proves that components/bench/bench-values.yaml adds bench-lean and bench-nomem and leaves `assistant` untouched: renders the
# chart with and without it and compares the four rendered `assistant` objects byte for byte. Nothing is installed.
#
#   CHART=/path/to/chart-copy components/bench/check-values.sh
#
# CHART defaults to charts/openagentix of the working tree; to test the committed chart use a copy exported from HEAD:
#   mkdir -p /tmp/oa && git archive HEAD charts/openagentix | tar -x -C /tmp/oa && CHART=/tmp/oa/charts/openagentix components/bench/check-values.sh
# BASE_VALUES = the values files you install with (space separated, same order), without the bench file.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
CHART=${CHART:-$ROOT/charts/openagentix}
BASE_VALUES=${BASE_VALUES:-"components/channels/demo-values.yaml components/schedules/demo-values.yaml components/notify/demo-values.yaml components/observability/demo-values.yaml components/knowledge/graph-demo-values.yaml"}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
cd "$ROOT"

args=()
for f in $BASE_VALUES; do args+=(-f "$f"); done

helm template openagentix "$CHART" "${args[@]}" > "$TMP/without.yaml"
helm template openagentix "$CHART" "${args[@]}" -f components/bench/bench-values.yaml > "$TMP/with.yaml"

# the four objects the chart renders for the agent `assistant`
pick() {
  awk -v RS='\n---\n' '/\nkind: (Agent|AgentTemplate|Harness|ModelConfig)\n/ && /\n  name: (assistant|assistant-template|assistant-harness|gateway-claude-haiku)\n/ {print $0 "\n---"}' "$1"
}
pick "$TMP/without.yaml" > "$TMP/assistant-without.yaml"
pick "$TMP/with.yaml" > "$TMP/assistant-with.yaml"
n=$(grep -c '^kind: ' "$TMP/assistant-with.yaml" || true)
[ "$n" -eq 4 ] || { echo "FAIL: expected 4 assistant objects, found $n" >&2; exit 1; }
cmp "$TMP/assistant-without.yaml" "$TMP/assistant-with.yaml" || { echo "FAIL: the rendered assistant changed" >&2; diff "$TMP/assistant-without.yaml" "$TMP/assistant-with.yaml" >&2 || true; exit 1; }
echo "OK: the 4 rendered assistant objects (Agent, AgentTemplate, Harness, ModelConfig) are byte-identical with and without bench-values.yaml ($(wc -c < "$TMP/assistant-with.yaml") bytes)"

removed=$({ diff "$TMP/without.yaml" "$TMP/with.yaml" || true; } | grep -c '^<' || true)
[ "$removed" -eq 0 ] || { echo "FAIL: $removed rendered lines were removed or changed by bench-values.yaml" >&2; exit 1; }
echo "OK: nothing else changed; objects added:"
{ diff "$TMP/without.yaml" "$TMP/with.yaml" || true; } | grep -E '^> (kind|  name):' | paste - - | sed 's/^> kind: //; s/>   name: /  /'
