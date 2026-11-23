#!/usr/bin/env bash
# dashboard.sh [--port N] [--no-open] [--window 1h|24h]      open the local dashboard
# dashboard.sh --check [--url BASE] [--window 1h|24h]        run every PromQL query of the page, exit 1 on a query error
#
# A dashboard without a new pod. It port-forwards the bundled VictoriaMetrics (and the Jaeger UI when it is there), serves the
# static page hack/dashboard/index.html on 127.0.0.1 and proxies the read-only Prometheus query API to it, so the page and its
# queries share one origin (no CORS involved) and the page makes no request to anything else. It needs python3 and curl, both on
# a stock macOS or Linux machine; no npm, no CDN.
#
#   hack/dashboard.sh                         # port-forward, serve on http://127.0.0.1:8765, open the browser
#   hack/dashboard.sh --no-open --port 9000
#   hack/dashboard.sh --check                 # port-forward and validate the page's queries against the live VictoriaMetrics
#   hack/dashboard.sh --check --url http://127.0.0.1:8428   # validate against an API you already reach (no kubectl needed)
#
# Needs observability.bundled.enabled (install with VALUES_FILE=components/observability/demo-values.yaml ./install.sh).
# Environment: KUBECTL (default kubectl), OBS_NAMESPACE (default openagentix-observability), VM_LOCAL_PORT and JAEGER_LOCAL_PORT
# (default: a free port). Nothing is written to the cluster; every process it starts is stopped on exit.
set -euo pipefail

K=${KUBECTL:-kubectl}
OBS_NS=${OBS_NAMESPACE:-openagentix-observability}
HERE=$(cd "$(dirname "$0")" && pwd)
SERVE=$HERE/dashboard/serve.py

PORT=8765
OPEN=1
CHECK=0
URL=""
WINDOW=1h

die() { printf 'dashboard: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --port) [ $# -ge 2 ] || usage 2; PORT=$2; shift 2 ;;
    --no-open) OPEN=0; shift ;;
    --check) CHECK=1; shift ;;
    --url) [ $# -ge 2 ] || usage 2; URL=$2; shift 2 ;;
    --window) [ $# -ge 2 ] || usage 2; WINDOW=$2; shift 2 ;;
    -h|--help) usage 0 ;;
    *) printf 'dashboard: unknown argument %s\n' "$1" >&2; usage 2 ;;
  esac
done
[[ "$PORT" =~ ^[0-9]+$ ]] || die "--port must be a number"
case "$WINDOW" in 1h|24h) ;; *) die "--window must be 1h or 24h" ;; esac
[ -z "$URL" ] || [ "$CHECK" -eq 1 ] || die "--url only goes with --check"
command -v python3 >/dev/null || die "python3 is required"

PIDS=()
TMP=$(mktemp -d "${TMPDIR:-/tmp}/oa-dashboard.XXXXXX")
# shellcheck disable=SC2329 # runs from the EXIT trap below
cleanup() {
  local p
  for p in ${PIDS[@]+"${PIDS[@]}"}; do kill "$p" 2>/dev/null || true; done
  for p in ${PIDS[@]+"${PIDS[@]}"}; do wait "$p" 2>/dev/null || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

if [ "$CHECK" -eq 1 ] && [ -n "$URL" ]; then
  python3 "$SERVE" check --url "$URL" --window "$WINDOW"
  exit $?
fi

command -v curl >/dev/null || die "curl is required"
command -v "$K" >/dev/null || die "$K is required (or use --check --url BASE against an API you can already reach)"
"$K" get namespace "$OBS_NS" >/dev/null 2>&1 ||
  die "namespace $OBS_NS not found. The dashboard reads the bundled VictoriaMetrics: install with observability.enabled and observability.bundled.enabled, for example VALUES_FILE=components/observability/demo-values.yaml ./install.sh"
"$K" get service victoria-metrics -n "$OBS_NS" >/dev/null 2>&1 ||
  die "service victoria-metrics not found in $OBS_NS. Set observability.bundled.enabled=true and run ./install.sh again (an own Prometheus works too: hack/dashboard.sh --check --url BASE validates the queries against it)"

freeport() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }

# forward <service> <remote port> <local port> <log file>: starts kubectl port-forward and records its pid
forward() {
  "$K" port-forward -n "$OBS_NS" "service/$1" "$3:$2" >"$4" 2>&1 &
  PIDS+=("$!")
  FORWARD_PID=$!
}

VM_PORT=${VM_LOCAL_PORT:-$(freeport)}
forward victoria-metrics 8428 "$VM_PORT" "$TMP/vm.log"
VM_PID=$FORWARD_PID
ready=0
for _ in $(seq 1 60); do
  if curl -fs "http://127.0.0.1:$VM_PORT/health" >/dev/null 2>&1; then ready=1; break; fi
  kill -0 "$VM_PID" 2>/dev/null || break
  sleep 0.25
done
if [ "$ready" -ne 1 ]; then
  cat "$TMP/vm.log" >&2 || true
  die "the port-forward to victoria-metrics did not become ready"
fi

if [ "$CHECK" -eq 1 ]; then
  python3 "$SERVE" check --url "http://127.0.0.1:$VM_PORT" --window "$WINDOW"
  exit $?
fi

JAEGER_ARGS=()
if "$K" get service jaeger -n "$OBS_NS" >/dev/null 2>&1; then
  JP=${JAEGER_LOCAL_PORT:-$(freeport)}
  forward jaeger 16686 "$JP" "$TMP/jaeger.log"
  JAEGER_ARGS=(--jaeger "http://127.0.0.1:$JP")
fi

python3 "$SERVE" serve --listen "127.0.0.1:$PORT" --upstream "http://127.0.0.1:$VM_PORT" ${JAEGER_ARGS[@]+"${JAEGER_ARGS[@]}"} >"$TMP/serve.log" 2>&1 &
PIDS+=("$!")
SERVE_PID=$!
up=0
for _ in $(seq 1 40); do
  if curl -fs "http://127.0.0.1:$PORT/config.json" >/dev/null 2>&1; then up=1; break; fi
  kill -0 "$SERVE_PID" 2>/dev/null || break
  sleep 0.25
done
if [ "$up" -ne 1 ]; then
  cat "$TMP/serve.log" >&2 || true
  die "could not serve the page on 127.0.0.1:$PORT (is the port in use? try --port)"
fi

PAGE="http://127.0.0.1:$PORT/"
printf 'dashboard: %s   (Ctrl-C to stop)\n' "$PAGE"
if [ "$OPEN" -eq 1 ]; then
  if command -v open >/dev/null; then open "$PAGE" >/dev/null 2>&1 || true
  elif command -v xdg-open >/dev/null; then xdg-open "$PAGE" >/dev/null 2>&1 || true
  fi
fi

# run until Ctrl-C, or until the page server or the port-forward goes away
while kill -0 "$SERVE_PID" 2>/dev/null && kill -0 "$VM_PID" 2>/dev/null; do sleep 1; done
die "the page server or the port-forward stopped"
