#!/usr/bin/env bash
# trace-turn.sh <contextId | traceId>
#
# Follow ONE turn through the platform without a log stack. Read-only: it only reads from the cluster.
#
#   contextId  the A2A contextId (= the kagent session id; the Substrate actor is session-<contextId>)
#   traceId    32 hex digits: the W3C trace id (the Telegram bridge logs it as trace_id, tools/a2a-call prints it as traceId)
#
# With the bundled Jaeger (observability.bundled.enabled) it reads the trace through `kubectl port-forward` and prints the hops
# with durations. Without it (or when no trace is found) it falls back to the egress proxy's log, joining the lines on
# ate.actor.name=session-<contextId>: the same hops, without model detail.
#
#   hack/trace-turn.sh 01a206cb-a49d-7ddd-b83f-88d5fcb243cf
#   hack/trace-turn.sh 4bf92f3577b34da6a3ce929d0e0e4736
#
# Environment: KUBECTL (default kubectl), OBS_NAMESPACE (default openagentix-observability), JAEGER_URL (query an existing
# Jaeger query API instead of port-forwarding), EGRESS_NAMESPACE (default ate-system), SINCE (log window for the fallback,
# default 6h). Nothing is written except to stdout; the port-forward it starts is stopped on exit.
set -euo pipefail

K=${KUBECTL:-kubectl}
OBS_NS=${OBS_NAMESPACE:-openagentix-observability}
EGRESS_NS=${EGRESS_NAMESPACE:-ate-system}
SINCE=${SINCE:-6h}
PORT=${JAEGER_LOCAL_PORT:-16687}

die() { printf 'trace-turn: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

[ $# -eq 1 ] || usage 2
case "$1" in -h|--help) usage 0 ;; esac
ID=$1
[[ "$ID" =~ ^[0-9a-fA-F-]{8,64}$ ]] || die "argument must be a contextId (uuid) or a 32-hex trace id"
for c in jq curl "$K"; do command -v "$c" >/dev/null || die "$c is required"; done

PF_PID=""
cleanup() { [ -z "$PF_PID" ] || kill "$PF_PID" 2>/dev/null || true; }
trap cleanup EXIT

# --- Jaeger ----------------------------------------------------------------------------------------------------------------
JAEGER=${JAEGER_URL:-}
if [ -z "$JAEGER" ] && "$K" get svc jaeger -n "$OBS_NS" >/dev/null 2>&1; then
  "$K" port-forward -n "$OBS_NS" svc/jaeger "$PORT:16686" >/dev/null 2>&1 &
  PF_PID=$!
  JAEGER="http://127.0.0.1:$PORT"
  for _ in $(seq 1 40); do curl -fs "$JAEGER/api/v3/services" >/dev/null 2>&1 && break; sleep 0.25; done
fi

# Jaeger v2: the search API is /api/v3 (OTLP JSON); a trace by id is read from the v1-style /api/traces/<id> (flat spans and
# processes), which is what the summary below uses.
TRACE_JSON=""
if [ -n "$JAEGER" ] && curl -fs "$JAEGER/api/v3/services" >/dev/null 2>&1; then
  tid=""
  if [[ "$ID" =~ ^[0-9a-f]{32}$ ]]; then
    tid=$ID
  else
    # a contextId: it is the gen_ai.conversation.id attribute on the agent runtime's spans (service name = agent name)
    attrs=$(jq -rn --arg id "$ID" '{"gen_ai.conversation.id":$id}|@json|@uri')
    from=$(date -u -v-3d +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '3 days ago' +%Y-%m-%dT%H:%M:%SZ)
    to=$(date -u -v+1d +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d 'tomorrow' +%Y-%m-%dT%H:%M:%SZ)
    for svc in $(curl -fs "$JAEGER/api/v3/services" | jq -r '.services[]'); do
      tid=$(curl -fs "$JAEGER/api/v3/traces?query.service_name=$svc&query.attributes=$attrs&query.start_time_min=$from&query.start_time_max=$to&query.num_traces=1" 2>/dev/null \
        | jq -r '[.result.resourceSpans[]?.scopeSpans[]?.spans[]?.traceId] | unique | .[0] // empty' 2>/dev/null || true)
      [ -z "$tid" ] || break
    done
  fi
  [ -z "$tid" ] || TRACE_JSON=$(curl -fs "$JAEGER/api/traces/$tid" 2>/dev/null || true)
  if [ -n "$TRACE_JSON" ] && [ "$(jq '(.data[0].spans|length)//0' <<<"$TRACE_JSON")" -eq 0 ]; then TRACE_JSON=""; fi
fi

if [ -n "$TRACE_JSON" ]; then
  jq -r --arg id "$ID" '
    def tag($k): (.tags // [] | map(select(.key == $k)) | .[0].value);
    def ms: (. / 1000 | floor);
    def secs: ((. / 100000 | floor) / 10 | tostring) + "s";
    . as $root | .data[0] as $t | $t.processes as $proc
    | ($t.spans | map(. + {svc: $proc[.processID].serviceName})) as $spans
    | ($spans | map(select(.operationName == "a2a.request" or (.operationName | startswith("invoke_agent ")))) | sort_by(.operationName != "a2a.request") | .[0]) as $a2a
    | ($spans | map(select(.operationName == "a2a.request")) | .[0]) as $req
    | ($spans | map(select(.svc == "kagent-controller" and (.operationName | startswith("POST /agents/")))) | .[0]) as $edge
    | ($spans | map(select(.svc == "model-gateway" and .operationName == "POST /v1/chat/completions"))) as $chat
    | ($spans | map(select(.svc == "model-gateway" and .operationName == "POST /v1/embeddings"))) as $emb
    | ($spans | map(select(.operationName | startswith("execute_tool ")))) as $tools
    | ($spans | map(select(.svc == "tools" and (.operationName | startswith("tools/call "))))) as $vmcp
    | ($spans | map(.startTime) | min) as $t0
    | ($spans | map(.startTime + .duration) | max) as $t1
    | "turn \(($a2a // {} | tag("gen_ai.conversation.id")) // $id)  trace \($t.traceID)  agent \(($a2a // {} | tag("gen_ai.agent.name")) // "?")  state \(($req // {} | tag("a2a.task.state")) // "n/a (the a2a.request span was not exported)")",
      "wall \(($edge // {duration: ($t1 - $t0)}).duration | secs)  spans \($spans | length) in \($spans | map(.svc) | unique | join(", "))",
      "",
      "model:  \($chat | length) call\(if ($chat | length) == 1 then "" else "s" end) \($chat | map(.duration) | add // 0 | secs)"
        + (if ($chat | length) > 0 then "  tokens in=\($chat | map(tag("gen_ai.usage.input_tokens") // 0) | add) out=\($chat | map(tag("gen_ai.usage.output_tokens") // 0) | add)  models=\($chat | map(tag("gen_ai.request.model")) | unique | join(","))  cost=$\($chat | map((tag("agw.ai.usage.cost.total") // 0) | tonumber) | add | . * 1000000 | round / 1000000)" else "" end),
      "memory: \($emb | length) embedding call\(if ($emb | length) == 1 then "" else "s" end) \($emb | map(.duration) | add // 0 | secs)",
      "tools:  " + (if ($tools | length) == 0 then "none" else ($tools | map((.operationName | sub("^execute_tool "; "")) + " " + (.duration | secs)
          + (if (tag("error.type") // tag("error")) != null then " ERROR" else "" end)) | join("; ")) end)
        + (if ($vmcp | length) > 0 then "   (vMCP: " + ($vmcp | map((.operationName | sub("^tools/call "; "")) + " " + (.duration | secs)) | unique | join("; ")) + ")" else "" end),
      "notify: " + (($tools | map(select(.operationName | test("notify_send$")))) as $n
          | if ($n | length) == 0 then "not called" else "sent via \($n | map(.operationName | sub("^execute_tool "; "")) | unique | join(",")) \($n | map(.duration) | add | secs)" + (if ($n | map(select((tag("error.type") // tag("error")) != null)) | length) > 0 then " (ERROR)" else "" end) end),
      "",
      "spans by service:",
      ($spans | group_by(.svc) | map("  \(.[0].svc): \(length)") | .[])
  ' <<<"$TRACE_JSON"
  exit 0
fi

# --- fallback: the egress proxy's log -----------------------------------------------------------------------------------------
if [[ "$ID" =~ ^[0-9a-f]{32}$ ]]; then
  pat="trace.id=$ID"
  what="trace $ID"
else
  pat="ate.actor.name=session-$ID"
  what="turn $ID"
fi
[ -n "$JAEGER" ] && printf 'no trace found in Jaeger for %s; using the egress proxy log (last %s)\n' "$what" "$SINCE" \
  || printf 'no Jaeger (observability.bundled.enabled is off); using the egress proxy log (last %s)\n' "$SINCE"
LOG=$("$K" logs -n "$EGRESS_NS" -l app=atenet-egress --since="$SINCE" --tail=-1 2>/dev/null | grep -F -- "$pat" || true)
[ -n "$LOG" ] || die "nothing found for $what in the egress log of $EGRESS_NS (a pod that is not a sandbox leaves no line there)"

awk '
  function field(k,   i, v) { v = ""; for (i = 1; i <= NF; i++) if (index($i, k "=") == 1) { v = substr($i, length(k) + 2); break } return v }
  {
    host = field("http.host"); path = field("http.path"); st = field("http.status"); d = field("duration")
    ms = d; sub(/ms$/, "", ms); if (d ~ /s$/ && d !~ /ms$/) { ms = d; sub(/s$/, "", ms); ms = ms * 1000 }
    kind = "other"
    if (path ~ /\/v1\/chat\/completions/) kind = "model"
    else if (path ~ /\/v1\/embeddings/) kind = "memory"
    else if (host ~ /^vmcp-/) kind = "tools"
    else if (path ~ /TaskStoreService|MemoryService/) kind = "kagent"
    n[kind]++; t[kind] += ms
    if (st != "" && st !~ /^2/) bad[kind]++
    if (!first) first = $1
    last = $1
  }
  END {
    printf "hops seen by the egress proxy (%s .. %s)\n", first, last
    split("model memory tools kagent other", order, " ")
    for (i = 1; i <= 5; i++) { k = order[i]; if (n[k] > 0) printf "%-7s %d call%s %.1fs%s\n", k ":", n[k], (n[k] == 1 ? "" : "s"), t[k] / 1000, (bad[k] ? "  non-2xx: " bad[k] : "") }
  }' <<<"$LOG"
