#!/usr/bin/env bash
# The timed runtime benchmark, kagent versus Google AX, in phases that run one at a time. See README.md for what is measured.
#
#   PHASE=plan components/bench/run.sh        print every phase, the model calls and rough tokens it will spend (no cluster access)
#   PHASE=a  components/bench/run.sh          then, one at a time, in this order (each is a foreground command, about 9 minutes at most):
#     a b c d1 d2 e          prompt groups, all backends interleaved, 5 runs each (d1 = runs 1-3, d2 = runs 4-5)
#     concurrency            K=4 identical plain questions at once, 2 rounds, kagent-lean and ax-api
#     cold-kagent            new session vs same session vs same session after an idle gap (kagent-lean)
#     cold-ax                first Task of a brand-new Workspace (snapshot build) vs the second (ax-api and ax-claude)
#     report                 tables from all CSVs and the per-turn breakdown (no model calls)
#
# Environment (defaults in brackets):
#   BACKENDS    [kagent kagent-nomem kagent-lean ax-claude ax-api]   backends for a b c d1 d2 e
#   RUNS        [5]            runs per group (d1/d2 split 3 + 2 and ignore it)
#   LOAD_MAX    [3.0]          wait until the node's 1-minute load average is below this before the phase
#   LOAD_TIMEOUT [180]         seconds to wait; then the phase starts anyway and the wait is recorded as timed out
#   OUT         [components/bench/out/<timestamp>]   output directory. Phases reuse the newest directory (.latest) when it is less
#                              than 12 hours old, so one campaign ends up in one place; NEW=1 forces a fresh one
#   WARMUP      [1]            1 = one untimed plain request per backend at the start of each phase (costs 1 model call per backend)
#   IDLE_GAP    [120]          seconds of idle between the turns in cold-kagent
#   AX_BIN      [<scratch>/ax] path of the `ax` CLI (cold-ax only; build it from the AX checkout copy: go build ./cmd/ax)
#   CTX, NODE, NS, POD         kubectl context [kind-openagentix], kind node container, client pod namespace and name [openagentix-gateway, cmp]
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
HARNESS=$ROOT/spikes/ax/compare/a2a_compare.py
CTX=${CTX:-kind-openagentix}
NODE=${NODE:-openagentix-control-plane}
NS=${NS:-openagentix-gateway}
POD=${POD:-cmp}
PHASE=${PHASE:-plan}
RUNS=${RUNS:-5}
LOAD_MAX=${LOAD_MAX:-3.0}
LOAD_TIMEOUT=${LOAD_TIMEOUT:-180}
WARMUP=${WARMUP:-1}
IDLE_GAP=${IDLE_GAP:-120}
BACKENDS=${BACKENDS:-"kagent kagent-nomem kagent-lean ax-claude ax-api"}
KAGENT_URL=http://kagent-controller.kagent.svc.cluster.local:8083/agents/kagent

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
die() { printf 'run.sh: %s\n' "$*" >&2; exit 1; }
k() { kubectl --context "$CTX" "$@"; }

# ---- backends ---------------------------------------------------------------------------------------------------------------
backend_url() {
  local ov=URL_${1//-/_}   # test hook: URL_ax_api=http://... replaces the URL of ax-api (used to dry-run this script without a model)
  if [ -n "${!ov:-}" ]; then echo "${!ov}"; return; fi
  case $1 in
    kagent) echo "$KAGENT_URL/assistant" ;;
    kagent-nomem) echo "$KAGENT_URL/bench-nomem" ;;
    kagent-lean) echo "$KAGENT_URL/bench-lean" ;;
    ax-claude) echo "http://ax-adapter.$NS.svc.cluster.local:8080/" ;;
    ax-api) echo "http://ax-adapter-b.$NS.svc.cluster.local:8080/" ;;
    *) die "unknown backend $1" ;;
  esac
}
backend_agent() { case $1 in kagent) echo assistant ;; kagent-nomem) echo bench-nomem ;; kagent-lean) echo bench-lean ;; esac; }

# model calls per run of a group (what the earlier comparison measured; ax-claude's canary run takes one extra Skill call)
calls_per_run() { # backend group
  case $2 in
    a) echo 1 ;; b) echo 2 ;; d) echo 3 ;; e) echo 2 ;;
    c) if [ "$1" = ax-claude ]; then echo 3; else echo 2; fi ;;
  esac
}
# rough total tokens per run (from the earlier comparison and the smoke tests; kagent-lean has no measurement yet, so it is a guess)
tokens_per_run() { # backend group
  case $1:$2 in
    kagent:a|kagent-nomem:a) echo 8000 ;;       kagent-lean:a) echo 3500 ;;  ax-claude:a) echo 9000 ;;   ax-api:a) echo 1200 ;;
    kagent:b|kagent-nomem:b) echo 28000 ;;      kagent-lean:b) echo 9000 ;;  ax-claude:b) echo 36000 ;;  ax-api:b) echo 11000 ;;
    kagent:c|kagent-nomem:c) echo 17000 ;;      kagent-lean:c) echo 7000 ;;  ax-claude:c) echo 20000 ;;  ax-api:c) echo 5000 ;;
    kagent:d|kagent-nomem:d) echo 30000 ;;      kagent-lean:d) echo 11000 ;; ax-claude:d) echo 30000 ;;  ax-api:d) echo 4000 ;;
    kagent:e|kagent-nomem:e) echo 16000 ;;      kagent-lean:e) echo 7000 ;;  ax-claude:e) echo 18000 ;;  ax-api:e) echo 2500 ;;
  esac
}

# phase -> "groups first runs minutes"
phase_spec() {
  case $1 in
    a) echo "a 1 $RUNS 5" ;; b) echo "b 1 $RUNS 7" ;; c) echo "c 1 $RUNS 4" ;; e) echo "e 1 $RUNS 8" ;;
    d1) echo "d 1 3 6" ;; d2) echo "d 4 2 4" ;;
    *) return 1 ;;
  esac
}

print_plan_for() { # phase backends...
  local ph=$1; shift
  local spec groups first runs mins b g total_calls=0 total_tokens=0 c t
  spec=$(phase_spec "$ph") || return 0
  read -r groups first runs mins <<<"$spec"
  printf '\nPHASE=%s  groups=%s runs=%s..%s  (about %s min)\n' "$ph" "$groups" "$first" "$((first + runs - 1))" "$mins"
  for b in "$@"; do
    for g in $groups; do
      c=$(( $(calls_per_run "$b" "$g") * runs )); t=$(( $(tokens_per_run "$b" "$g") * runs ))
      printf '  %-13s group %s: %3d model calls, ~%7d tokens\n' "$b" "$g" "$c" "$t"
      total_calls=$((total_calls + c)); total_tokens=$((total_tokens + t))
    done
    if [ "$WARMUP" = 1 ]; then total_calls=$((total_calls + 1)); total_tokens=$((total_tokens + $(tokens_per_run "$b" a))); fi
  done
  printf '  total: %d model calls (warm-up %s), ~%d tokens. The memory-ON backend (kagent) may add memory-save calls on the memory key.\n' \
    "$total_calls" "$([ "$WARMUP" = 1 ] && echo "included: $# calls" || echo "off")" "$total_tokens"
}

print_plan() {
  echo "Benchmark plan. Backends: $BACKENDS. Estimates are rough; tokens are what each backend reports, summed over a run."
  local ph list
  read -r -a list <<<"$BACKENDS"
  for ph in a b c d1 d2 e; do print_plan_for "$ph" "${list[@]}"; done
  cat <<EOF

PHASE=concurrency  K=4 x 2 rounds, backends kagent-lean and ax-api, plain question: 2 x 2 x 4 = 16 model calls (+2 warm-up), ~65,000 tokens (about 4 min)
PHASE=cold-kagent  kagent-lean: 2 x (new session, same session at once) + 2 x (new session, same session after ${IDLE_GAP}s idle) = 8 calls (about $(((40 + 2 * (20 + IDLE_GAP)) / 60 + 1)) min)
PHASE=cold-ax      ax-api and ax-claude: 2 cycles each of (new Workspace: first Task, second Task) = 8 calls, plus 4 Workspace snapshot builds (no model calls) (about 8 min)
PHASE=report       no model calls
EOF
}

# ---- node load --------------------------------------------------------------------------------------------------------------
node_load() { docker exec "$NODE" cat /proc/loadavg | awk '{print $1}'; }

wait_load() {
  local t0 l
  t0=$(date +%s)
  while :; do
    l=$(node_load)
    if awk -v l="$l" -v m="$LOAD_MAX" 'BEGIN{exit !(l<m)}'; then
      log "node load $l < $LOAD_MAX"
      echo "$(date -u +%FT%TZ) phase=$PHASE load=$l waited=$(($(date +%s) - t0))s result=ok" >> "$OUT/load-waits.log"
      return 0
    fi
    if [ $(($(date +%s) - t0)) -ge "$LOAD_TIMEOUT" ]; then
      log "node load $l still >= $LOAD_MAX after ${LOAD_TIMEOUT}s: starting anyway (recorded)"
      echo "$(date -u +%FT%TZ) phase=$PHASE load=$l waited=$(($(date +%s) - t0))s result=TIMED_OUT" >> "$OUT/load-waits.log"
      return 0
    fi
    sleep 10
  done
}

# ---- output directory and checks ----------------------------------------------------------------------------------------------
setup_out() {
  local latest=$HERE/out/.latest
  if [ -z "${OUT:-}" ]; then
    if [ "${NEW:-0}" != 1 ] && [ -f "$latest" ] && [ -d "$(cat "$latest")" ] && [ -n "$(find "$latest" -mmin -720 2>/dev/null)" ]; then
      OUT=$(cat "$latest")
    else
      OUT=$HERE/out/$(date +%Y%m%dT%H%M%S)
    fi
  fi
  mkdir -p "$OUT" "$HERE/out"
  echo "$OUT" > "$latest"
  log "output directory: $OUT"
}

preflight() { # backends...
  k -n "$NS" get pod "$POD" -o jsonpath='{.status.phase}' 2>/dev/null | grep -q Running || die "client pod $NS/$POD is not Running (see README, Install)"
  local b a
  for b in "$@"; do
    case $b in
      kagent*)
        a=$(backend_agent "$b")
        [ "$(k -n kagent get agent "$a" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = True ] \
          || die "kagent agent $a is not Ready (bench agents need components/bench/bench-values.yaml installed; or drop $b from BACKENDS)" ;;
      ax-claude) k -n "$NS" get deploy ax-adapter >/dev/null 2>&1 || die "deployment ax-adapter is missing (README, Install)" ;;
      ax-api) k -n "$NS" get deploy ax-adapter-b >/dev/null 2>&1 || die "deployment ax-adapter-b is missing (README, Install)" ;;
    esac
  done
}

record_env() {
  [ -f "$OUT/env.txt" ] && return 0
  {
    echo "date $(date -u +%FT%TZ)"
    echo "node load $(node_load)"
    echo "pods $(k get pods -A --no-headers | wc -l | tr -d ' ')"
    echo "node memory $(docker stats --no-stream "$NODE" --format '{{.MemUsage}}')"
    echo "model $(k -n kagent get modelconfig gateway-claude-haiku -o jsonpath='{.spec.model}' 2>/dev/null)  (AX agents: claude-haiku-5-5 straight to api.anthropic.com)"
    echo "agents:"; k -n kagent get agents --no-headers 2>/dev/null | awk '{print "  " $1 " ready=" $0}' | cut -c1-120
    echo "ax pods:"; k -n ax-system get pods --no-headers | awk '{print "  " $1 " " $3}'
  } > "$OUT/env.txt" 2>&1 || true
}

# ---- harness ------------------------------------------------------------------------------------------------------------------
backends_arg() { # backends... -> name=url,name=url
  local b out=""
  for b in "$@"; do out="${out:+$out,}$b=$(backend_url "$b")"; done
  echo "$out"
}

header_args() { # backends... -> --header ... for every kagent backend (distinct X-User-Id per run, so no memory or session carries over)
  local b
  for b in "$@"; do case $b in kagent*) printf -- '--header\n%s:X-User-Id:bench:{backend}:{rid}\n' "$b" ;; esac; done
}

harness() { # args... ; runs the harness inside the client pod, CSV on stdout
  k exec -i -n "$NS" "$POD" -- python3 - "$@" < "$HARNESS"
}

seq_phase() { # phase
  local ph=$1 spec groups first runs mins g r i=0 load
  spec=$(phase_spec "$ph") || die "no such phase $ph"
  read -r groups first runs mins <<<"$spec"
  # shellcheck disable=SC2086
  set -- $BACKENDS
  preflight "$@"
  print_plan_for "$ph" "$@"
  wait_load
  record_env
  local csv=$OUT/results-$ph.csv ans=/tmp/answers-$ph.jsonl rid hdr=()
  rid=$(date +%Y%m%d%H%M%S)
  while IFS= read -r line; do [ -z "$line" ] || hdr+=("$line"); done < <(header_args "$@")
  k exec -n "$NS" "$POD" -- rm -f "$ans"
  : > "$csv"
  for g in $groups; do
    for r in $(seq "$first" $((first + runs - 1))); do
      load=$(node_load)
      local extra=()
      [ "$i" -gt 0 ] && extra+=(--no-header)
      [ "$i" -eq 0 ] && [ "$WARMUP" = 1 ] && extra+=(--warmup)
      log "phase $ph group $g run $r (load $load)"
      harness --backends "$(backends_arg "$@")" --groups "$g" --first "$r" --runs 1 --run-id "$rid" --load "$load" --answers "$ans" \
        ${hdr[@]+"${hdr[@]}"} ${extra[@]+"${extra[@]}"} >> "$csv" 2>> "$OUT/harness-$ph.log"
      i=$((i + 1))
    done
  done
  k exec -n "$NS" "$POD" -- cat "$ans" > "$OUT/answers-$ph.jsonl"
  python3 "$ROOT/spikes/ax/compare/summarize.py" "$csv"
  log "done: $csv"
}

concurrency_phase() {
  local conc=${CONC_BACKENDS:-"kagent-lean ax-api"}
  # shellcheck disable=SC2086
  set -- $conc
  preflight "$@"
  wait_load
  record_env
  local hdr=() warm=() ans=/tmp/answers-concurrency.jsonl csv=$OUT/results-concurrency.csv
  [ "$WARMUP" = 1 ] && warm=(--warmup)
  while IFS= read -r line; do [ -z "$line" ] || hdr+=("$line"); done < <(header_args "$@")
  k exec -n "$NS" "$POD" -- rm -f "$ans"
  log "concurrency: K=4, 2 rounds, backends $conc (the AX adapter admits MAX_CONCURRENT_TASKS=2 at once; each pool has ONE worker)"
  harness --backends "$(backends_arg "$@")" --concurrency 4 --rounds 2 --run-id "$(date +%Y%m%d%H%M%S)" --load "$(node_load)" --answers "$ans" \
    ${warm[@]+"${warm[@]}"} ${hdr[@]+"${hdr[@]}"} > "$csv" 2>> "$OUT/harness-concurrency.log"
  k exec -n "$NS" "$POD" -- cat "$ans" > "$OUT/answers-concurrency.jsonl"
  python3 "$ROOT/spikes/ax/compare/summarize.py" "$csv"
}

cold_kagent_phase() {
  local b=${COLD_KAGENT_BACKEND:-kagent-lean} rid hdr=() ans=/tmp/answers-cold-kagent.jsonl csv=$OUT/results-cold-kagent.csv
  preflight "$b"
  wait_load
  record_env
  rid=$(date +%Y%m%d%H%M%S)
  while IFS= read -r line; do [ -z "$line" ] || hdr+=("$line"); done < <(header_args "$b")
  k exec -n "$NS" "$POD" -- rm -f "$ans"
  log "cold-kagent on $b: turn 1 = a NEW session (a new actor is created from the agent's golden snapshot and resumed); turn 2 = same session"
  # runs 1-2: turn 2 immediately; runs 3-4: turn 2 after IDLE_GAP seconds of idle. Group e = plain question, then another plain question.
  harness --backends "$(backends_arg "$b")" --groups e --first 1 --runs 2 --run-id "$rid-now" --load "$(node_load)" --answers "$ans" ${hdr[@]+"${hdr[@]}"} > "$csv" 2>> "$OUT/harness-cold-kagent.log"
  harness --backends "$(backends_arg "$b")" --groups e --first 3 --runs 2 --turn-gap "$IDLE_GAP" --run-id "$rid-idle" --load "$(node_load)" --answers "$ans" \
    ${hdr[@]+"${hdr[@]}"} --no-header >> "$csv" 2>> "$OUT/harness-cold-kagent.log"
  k exec -n "$NS" "$POD" -- cat "$ans" > "$OUT/answers-cold-kagent.jsonl"
  cut -d, -f2-5,8,10-14 "$csv" | column -s, -t | cut -c1-200
}

cold_ax_phase() {
  local AX=${AX_BIN:-}
  [ -x "$AX" ] || die "set AX_BIN to the ax CLI (go build ./cmd/ax in the AX checkout copy)"
  preflight ax-api ax-claude
  wait_load
  record_env
  local csv=$OUT/results-cold-ax.csv ans=/tmp/answers-cold-ax.jsonl rid variant ws url nonce cycle name hdrflag=()
  rid=$(date +%Y%m%d%H%M%S)
  k exec -n "$NS" "$POD" -- rm -f "$ans"
  : > "$csv"
  for variant in ax-api ax-claude; do
    case $variant in ax-api) ws=assistant-ws-api ;; ax-claude) ws=assistant-ws-v1 ;; esac
    for cycle in 1 2; do
      nonce=$(date +%s)-$cycle
      name=$ws-cold$cycle
      log "cold-ax $variant cycle $cycle: new Workspace $name (setup file with a fresh nonce, so no snapshot exists), own adapter"
      # shellcheck disable=SC2016
      "$AX" --context "$CTX" get workspace "$ws" \
        | yq -y --arg n "$name" --arg nonce "$nonce" 'del(.metadata.createTime,.metadata.uid,.metadata.updateTime) | .metadata.name=$n
            | .spec.setup.files += [{"path":".agent/cold-nonce","content":$nonce}]' \
        | "$AX" --context "$CTX" apply -f - > /dev/null
      adapter_yaml "ax-adapter-cold" "$name" | k apply -f - > /dev/null
      k -n "$NS" rollout status deploy/ax-adapter-cold --timeout=120s > /dev/null
      url=http://ax-adapter-cold.$NS.svc.cluster.local:8080/
      # two plain questions on a NEW conversation each: the first Task builds the Workspace snapshot, the second restores it
      harness --backends "$variant-cold=$url" --groups a --first "$cycle" --runs 1 --run-id "$rid" --load "$(node_load)" --answers "$ans" \
        ${hdrflag[@]+"${hdrflag[@]}"} >> "$csv" 2>> "$OUT/harness-cold-ax.log"
      hdrflag=(--no-header)
      harness --backends "$variant-warm=$url" --groups a --first "$cycle" --runs 1 --run-id "$rid" --load "$(node_load)" --answers "$ans" --no-header \
        >> "$csv" 2>> "$OUT/harness-cold-ax.log"
      k -n "$NS" delete deploy/ax-adapter-cold svc/ax-adapter-cold --wait=true > /dev/null
      "$AX" --context "$CTX" delete workspace "$name" > /dev/null || log "could not delete workspace $name (delete it by hand)"
    done
  done
  "$AX" --context "$CTX" tunnel stop > /dev/null 2>&1 || true   # the ax CLI leaves a background port-forward behind
  k exec -n "$NS" "$POD" -- cat "$ans" > "$OUT/answers-cold-ax.jsonl"
  cut -d, -f2,4,8,10,11,14,19 "$csv" | column -s, -t | cut -c1-200
}

adapter_yaml() { # name workspace -> a copy of bridges/ax-adapter/deploy/deployment-b.yaml under another name and Workspace
  cat <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: $1, namespace: $NS, labels: {app: $1, app.kubernetes.io/part-of: bench}}
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: $1}}
  template:
    metadata: {labels: {app: $1}}
    spec:
      automountServiceAccountToken: false
      securityContext: {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, seccompProfile: {type: RuntimeDefault}}
      containers:
        - name: adapter
          image: openagentix/ax-adapter:dev
          imagePullPolicy: IfNotPresent
          ports: [{name: http, containerPort: 8080}]
          env:
            - {name: LISTEN_ADDR, value: ":8080"}
            - {name: AX_SERVER, value: "ax-server.ax-system.svc.cluster.local:8080"}
            - {name: ATESPACE, value: "default"}
            - {name: AX_WORKSPACE, value: "$2"}
            - {name: AX_GATEWAY, value: "anthropic-gw"}
            - {name: TURN_TIMEOUT, value: "170s"}
            - {name: KEEP_TASKS, value: "false"}
          readinessProbe: {httpGet: {path: /healthz, port: http}, periodSeconds: 3}
          securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
          resources: {requests: {cpu: 10m, memory: 24Mi}, limits: {cpu: 200m, memory: 96Mi}}
---
apiVersion: v1
kind: Service
metadata: {name: $1, namespace: $NS, labels: {app: $1}}
spec:
  selector: {app: $1}
  ports: [{name: http, port: 8080, targetPort: http}]
EOF
}

report_phase() {
  local csvs=()
  while IFS= read -r f; do csvs+=("$f"); done < <(find "$OUT" -maxdepth 1 -name 'results-*.csv' | sort)
  [ "${#csvs[@]}" -gt 0 ] || die "no results-*.csv in $OUT"
  python3 "$ROOT/spikes/ax/compare/summarize.py" "${csvs[@]}" | tee "$OUT/summary.md"
  log "per-turn breakdown (reads Jaeger, the egress proxy log and the ax-server log; no model calls)"
  python3 "$HERE/trace-breakdown.py" --context "$CTX" --rows-out "$OUT/breakdown-rows.csv" "${csvs[@]}" | tee "$OUT/breakdown.md"
  log "wrote $OUT/summary.md and $OUT/breakdown.md"
}

# ---- main ---------------------------------------------------------------------------------------------------------------------
if [ "$PHASE" = plan ]; then print_plan; exit 0; fi
for c in kubectl docker yq python3 awk; do command -v "$c" > /dev/null || die "$c is required"; done
setup_out
case $PHASE in
  a|b|c|d1|d2|e) seq_phase "$PHASE" ;;
  concurrency) concurrency_phase ;;
  cold-kagent) cold_kagent_phase ;;
  cold-ax) cold_ax_phase ;;
  report) report_phase ;;
  *) die "unknown PHASE=$PHASE (plan a b c d1 d2 e concurrency cold-kagent cold-ax report)" ;;
esac
