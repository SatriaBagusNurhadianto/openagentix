#!/usr/bin/env bash
# Offline tests of hack/dashboard.sh and hack/dashboard/: a fake Prometheus API (python3), a fake kubectl, the real proxy server
# and a jsdom run of the page (jsdom from website/node_modules; skipped when it is not installed).
#   hack/dashboard/tests/run.sh        (exit 1 on any failure)
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
SH=$ROOT/hack/dashboard.sh
PAGE=$ROOT/hack/dashboard/index.html
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  FAIL %s\n       %s\n' "$1" "${2:-}"; }
t()   { if eval "$2"; then ok "$1"; else bad "$1" "$2"; fi; }   # t <description> <shell condition>

TMP=$(mktemp -d "${TMPDIR:-/tmp}/oa-dash-test.XXXXXX")
PIDS=()
cleanup() { for p in ${PIDS[@]+"${PIDS[@]}"}; do kill "$p" 2>/dev/null; done; wait 2>/dev/null; rm -rf "$TMP"; }
trap cleanup EXIT
freeport() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
waitfor() { for _ in $(seq 1 80); do curl -fs "$1" >/dev/null 2>&1 && return 0; sleep 0.1; done; return 1; }
has() { printf '%s\n' "$OUT" | /usr/bin/grep -q "$1"; }
alive() { pgrep -f "$1" >/dev/null 2>&1; }

echo "# the page itself"
t "shellcheck is clean on dashboard.sh and these tests" "shellcheck -x '$SH' '$HERE/run.sh' '$HERE/fake-kubectl'"
t "no external reference in the page: no http(s) URL, no src, no link, no import, no url()" "! /usr/bin/grep -Eqi 'https?://|//cdn|<link|<script[^>]* src=|@import|url\\(|<img|<iframe' '$PAGE'"
t "light and dark themes via prefers-color-scheme" "/usr/bin/grep -q 'prefers-color-scheme: dark' '$PAGE' && /usr/bin/grep -q 'color-scheme: light dark' '$PAGE'"
t "the queries block is valid JSON with unique ids" "python3 -c \"import json,re,sys;h=open('$PAGE').read();q=json.loads(re.search(r'id=\\\"queries\\\">(.*?)</script>',h,re.S).group(1));ids=[x['id'] for x in q];sys.exit(0 if len(ids)==len(set(ids))>=14 and all(x['kind'] in ('instant','range') for x in q) else 1)\""
t "every panel title of the brief is present" "for w in 'Cost per agent' 'Tokens per agent' 'Model requests and errors' 'Tool calls by tool' 'Knowledge base searches' 'Graph extraction' 'Notifications' 'Scrape target health'; do /usr/bin/grep -q \"\$w\" '$PAGE' || exit 1; done"
t "the cost panel states the VictoriaMetrics first-sample quirk" "/usr/bin/grep -q 'first sample as an increase' '$PAGE'"

echo "# --check against a fake Prometheus API"
LOG=$TMP/req.log
FP=$(freeport)
python3 "$HERE/fake_prom.py" --port "$FP" --log "$LOG" >/dev/null & PIDS+=("$!")
waitfor "http://127.0.0.1:$FP/health" || bad "fake API did not start"
OUT=$("$SH" --check --url "http://127.0.0.1:$FP" 2>&1); RC=$?
t "all queries ok: exit 0" "[ $RC -eq 0 ]"
t "it ran every query of the page" "[ \$(python3 -c \"import json,re;h=open('$PAGE').read();print(len(json.loads(re.search(r'id=\\\"queries\\\">(.*?)</script>',h,re.S).group(1))))\") -eq \$(echo \"\$OUT\" | /usr/bin/grep -c '^ok ') ]"
t "no placeholder is left in a query it sent" "! /usr/bin/grep -q '%7B%7BW%7D%7D\\|%7B%7BR%7D%7D' '$LOG'"
FQ=$(freeport)
python3 "$HERE/fake_prom.py" --port "$FQ" --fail "toolhive_mcp_tool_calls_total" >/dev/null & PIDS+=("$!")
waitfor "http://127.0.0.1:$FQ/health" || bad "second fake API did not start"
OUT=$("$SH" --check --url "http://127.0.0.1:$FQ" 2>&1); RC=$?
t "a query the API rejects: non-zero exit" "[ $RC -ne 0 ]"
t "the failing query is named with the API's message" "has 'FAIL  tools' && has 'parse error'"
FE=$(freeport)
python3 "$HERE/fake_prom.py" --port "$FE" --empty >/dev/null & PIDS+=("$!")
waitfor "http://127.0.0.1:$FE/health" || bad "empty fake API did not start"
OUT=$("$SH" --check --url "http://127.0.0.1:$FE" 2>&1); RC=$?
t "no data at all is not an error: exit 0, said plainly" "[ $RC -eq 0 ] && has 'no data yet'"
"$SH" --check --url "http://127.0.0.1:$(freeport)" >/dev/null 2>&1; RC=$?
t "an unreachable API: non-zero exit" "[ $RC -ne 0 ]"
"$SH" --check --url "http://127.0.0.1:$FP" --window 24h >/dev/null 2>&1; RC=$?
t "--window 24h is honoured" "[ $RC -eq 0 ] && /usr/bin/grep -q 'cost_usd_total%5B24h%5D' '$LOG'"
t "--url without --check is refused" "! \"$SH\" --url http://127.0.0.1:1 >/dev/null 2>&1"
t "a bad --window is refused" "! \"$SH\" --check --url http://127.0.0.1:$FP --window 5y >/dev/null 2>&1"

echo "# refuses to run without the observability namespace or service"
export KUBECTL=$HERE/fake-kubectl
OUT=$(FAKE_NS_MISSING=1 "$SH" --no-open 2>&1); RC=$?
t "namespace missing: non-zero, names the namespace and the way to install it" "[ $RC -ne 0 ] && has 'openagentix-observability' && has 'observability.bundled.enabled'"
OUT=$(FAKE_SVC_MISSING=1 "$SH" --no-open 2>&1); RC=$?
t "service missing: non-zero, names the service and the value to set" "[ $RC -ne 0 ] && has 'victoria-metrics' && has 'observability.bundled.enabled=true'"
OUT=$(FAKE_NS_MISSING=1 "$SH" --check 2>&1); RC=$?
t "--check without --url refuses the same way" "[ $RC -ne 0 ] && has 'not found'"

echo "# --check through the port-forward (fake kubectl)"
VP=$(freeport)
VM_LOCAL_PORT=$VP "$SH" --check >/dev/null 2>&1; RC=$?
t "port-forward then check: exit 0" "[ $RC -eq 0 ]"
t "the port-forward is gone after --check" "! alive 'fake_prom.py --port $VP'"

# serve <port> <extra env...>: runs hack/dashboard.sh in the background
serve() {
  local port=$1; shift
  VM_LOCAL_PORT=$VP env "$@" "$SH" --no-open --port "$port" >"$TMP/serve.out" 2>&1 &
  SERVE_PID=$!
  waitfor "http://127.0.0.1:$port/config.json"
}
stop_serve() { kill -TERM "$SERVE_PID" 2>/dev/null; wait "$SERVE_PID" 2>/dev/null; }

echo "# the page server"
SP=$(freeport); VP=$(freeport)
serve "$SP" FAKE_JAEGER=1 || bad "dashboard did not start" "$(cat "$TMP/serve.out")"
B=http://127.0.0.1:$SP
t "GET / serves the page" "curl -fs $B/ | /usr/bin/grep -q 'OpenAgentix dashboard'"
t "the page carries a Content-Security-Policy that forbids anything but its own origin" "curl -fsI $B/ | tr -d '\\r' | /usr/bin/grep -i '^content-security-policy:' | /usr/bin/grep -q \"default-src 'none'.*connect-src 'self'\""
t "no CORS header is sent (the page is same-origin)" "! curl -fsI $B/ | /usr/bin/grep -qi '^access-control-'"
t "the query API is proxied" "curl -fs '$B/api/v1/query?query=up' | python3 -c 'import json,sys;d=json.load(sys.stdin);sys.exit(0 if d[\"status\"]==\"success\" and len(d[\"data\"][\"result\"])==3 else 1)'"
t "a range query is proxied" "curl -fs '$B/api/v1/query_range?query=up&start=1&end=2&step=1' | /usr/bin/grep -q success"
t "an API path off the allowlist is not proxied (404)" "[ \$(curl -s -o /dev/null -w '%{http_code}' $B/api/v1/admin/tsdb/delete_series) = 404 ]"
t "writes are refused (405)" "[ \$(curl -s -o /dev/null -w '%{http_code}' -X POST $B/api/v1/query -d x) = 405 ] && [ \$(curl -s -o /dev/null -w '%{http_code}' -X DELETE $B/api/v1/query) = 405 ]"
t "a foreign Host header is refused (DNS rebinding)" "[ \$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example' $B/) = 403 ]"
t "an unknown path is 404" "[ \$(curl -s -o /dev/null -w '%{http_code}' $B/etc/passwd) = 404 ]"
t "the Jaeger link is configured when the service exists" "curl -fs $B/config.json | /usr/bin/grep -Eq '\"jaeger\": \"http://127.0.0.1:[0-9]+\"'"
if [ -f "$ROOT/website/node_modules/jsdom/package.json" ]; then
  echo "# the page in jsdom (data)"
  node "$HERE/smoke.js" "$B" data; RC=$?
  t "smoke test: panels render with data, nothing leaves localhost" "[ $RC -eq 0 ]"
else
  echo "  skip jsdom is not installed (cd website && npm ci)"
fi
t "the Jaeger port-forward is running while the dashboard runs" "alive 'http.server [0-9]* --bind 127.0.0.1'"
stop_serve
t "stopping the script ends the page server" "! alive 'serve.py serve --listen 127.0.0.1:$SP'"
t "stopping the script ends the port-forward" "! alive 'fake_prom.py --port $VP'"
t "stopping the script ends the Jaeger port-forward" "! alive 'http.server [0-9]* --bind 127.0.0.1'"
t "no temp directory is left behind by the script" "[ -z \"\$(ls -d \${TMPDIR:-/tmp}/oa-dashboard.* 2>/dev/null)\" ]"
SP=$(freeport); VP=$(freeport)
serve "$SP" || bad "dashboard did not start (no jaeger)"
t "no Jaeger link without the service" "curl -fs http://127.0.0.1:$SP/config.json | /usr/bin/grep -q '\"jaeger\": null'"
stop_serve

if [ -f "$ROOT/website/node_modules/jsdom/package.json" ]; then
  for mode in empty error; do
    echo "# the page in jsdom ($mode)"
    SP=$(freeport); VP=$(freeport)
    if [ "$mode" = empty ]; then ARGS="--empty"; else ARGS="--fail kb_search_requests_total"; fi
    serve "$SP" FAKE_PROM_ARGS="$ARGS" || bad "dashboard did not start ($mode)"
    node "$HERE/smoke.js" "http://127.0.0.1:$SP" "$mode"; RC=$?
    t "smoke test ($mode)" "[ $RC -eq 0 ]"
    stop_serve
  done
fi

echo
echo "passed=$PASS failed=$FAIL"
[ "$FAIL" -eq 0 ]
