#!/usr/bin/env bash
# Regenerates testdata/golden-profile-demo.json and golden-assistant.json: the kagent objects `helm template` renders for the
# demo agents `profile-demo` and `assistant` (the demo values files of install.sh, in the order its header comment gives). The Go parity test
# (internal/adapters/kagent) renders the same agent from an AgentDefinition and compares field by field.
# Needs: helm, yq (jq wrapper), jq. Run from anywhere.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
cd "$ROOT"
args=()
for f in \
  components/channels/demo-values.yaml \
  components/schedules/demo-values.yaml \
  components/notify/demo-values.yaml \
  components/observability/demo-values.yaml \
  components/knowledge/graph-demo-values.yaml \
  components/governance/demo-values.yaml \
  components/governance/profiles-demo-values.yaml \
  components/governance/memory-demo-values.yaml; do
  args+=(-f "$f")
done
RENDERED="$(helm template openagentix charts/openagentix -n openagentix-system "${args[@]}" | yq -c 'select(.!=null)')"
# golden <agent> <its ModelConfig name> <output file>: the ModelConfig, Harness, AgentTemplate, EgressPolicy (when there is one)
# and Agent of one chart agent, in that order.
golden() {
  jq -s --arg a "$1" --arg mc "$2" --argjson order '["ModelConfig","Harness","AgentTemplate","EgressPolicy","Agent"]' '
      [ .[] | select(.metadata.namespace=="kagent"
          and (   (.kind=="ModelConfig"    and .metadata.name==$mc)
               or (.kind=="Harness"        and .metadata.name==($a+"-harness"))
               or (.kind=="AgentTemplate"  and .metadata.name==($a+"-template"))
               or (.kind=="EgressPolicy"   and .metadata.name==($a+"-egress"))
               or (.kind=="Agent"          and .metadata.name==$a)) ) ]
      | sort_by(.kind as $k | $order | index($k))' <<<"$RENDERED" > "$3"
  echo "wrote $3: $(jq length "$3") objects"
}
golden profile-demo gateway-profile-demo "$HERE/golden-profile-demo.json"
# the default agent: memory, knowledge with graph, notify, a skill, no profile, a ModelConfig name set in values
golden assistant gateway-claude-haiku "$HERE/golden-assistant.json"
