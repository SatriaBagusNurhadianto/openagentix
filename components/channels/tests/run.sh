#!/usr/bin/env bash
# Offline tests of how kourier.sh decides which API server addresses Agent Kourier may reach. No cluster, no network: the
# function in ../apiserver-cidrs.sh is tested on fixtures, and `kourier.sh install` is run once against a fake kubectl and a
# fake helm to prove the wiring. Needs: jq, yq (the jq wrapper kourier.sh uses), bash.
# Usage: components/channels/tests/run.sh   (exit 1 on any failure)
set -u
HERE="$(cd "$(dirname "$0")/.." && pwd)"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  FAIL %s\n       expected: %s\n       actual:   %s\n' "$1" "$2" "$3"; }
eq()  { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "$3" "$2"; fi; }   # eq desc actual expected

# shellcheck source=/dev/null
. "$HERE/apiserver-cidrs.sh"

# fixtures: the parts of kubectl's JSON the function reads
eps()   { local a; a=$(printf '"%s",' "$@"); printf '{"items":[{"endpoints":[{"addresses":[%s]}]}]}' "${a%,}"; }
nodes() { local n=""; for p in "$@"; do n+="{\"spec\":{\"providerID\":\"$p\"}},"; done; printf '{"items":[%s]}' "${n%,}"; }
KIND=$(nodes "kind://docker/openagentix/openagentix-control-plane")
KIND3=$(nodes "kind://docker/c/c-control-plane" "kind://docker/c/c-worker" "kind://docker/c/c-worker2")
AWS=$(nodes "aws:///eu-west-1a/i-0abc" "aws:///eu-west-1b/i-0def")
# derive <override> <eps> <nodes> -> compact JSON array, or "ERROR" with the message in $ERR
derive() { ERR=$(kourier_apiserver_cidrs "$@" 2>&1 >/dev/null) || { echo ERROR; return; }; kourier_apiserver_cidrs "$@" 2>/dev/null; }

echo "# kind: the node subnet, so an address change after a Docker restart does not cut Kourier off"
eq "kind node 172.18.0.2 -> its /24"                     "$(derive '' "$(eps 172.18.0.2)" "$KIND")" '["172.18.0.0/24"]'
eq "the same cluster after a restart (.3) -> same rule"  "$(derive '' "$(eps 172.18.0.3)" "$KIND")" '["172.18.0.0/24"]'
eq "the rule covers the old and the new address"         "$(derive '' "$(eps 172.18.0.2)" "$KIND")" "$(derive '' "$(eps 172.18.0.3)" "$KIND")"
eq "three kind nodes"                                    "$(derive '' "$(eps 172.18.0.5)" "$KIND3")" '["172.18.0.0/24"]'
eq "two addresses in one /24 give one rule"              "$(derive '' "$(eps 172.18.0.2 172.18.0.4)" "$KIND3")" '["172.18.0.0/24"]'
eq "two /24s give two rules, sorted"                     "$(derive '' "$(eps 172.19.1.2 172.18.0.2)" "$KIND")" '["172.18.0.0/24","172.19.1.0/24"]'

echo "# other clusters: the exact endpoint addresses"
eq "provider id aws -> /32"                              "$(derive '' "$(eps 10.0.0.5)" "$AWS")" '["10.0.0.5/32"]'
eq "three control-plane addresses, sorted, unique"       "$(derive '' "$(eps 10.0.0.7 10.0.0.5 10.0.0.5 10.0.0.6)" "$AWS")" '["10.0.0.5/32","10.0.0.6/32","10.0.0.7/32"]'
eq "no node list -> not kind -> /32"                     "$(derive '' "$(eps 172.18.0.2)" '')" '["172.18.0.2/32"]'
eq "empty node list -> not kind -> /32"                  "$(derive '' "$(eps 172.18.0.2)" '{"items":[]}')" '["172.18.0.2/32"]'
eq "a node without provider id -> not kind -> /32"       "$(derive '' "$(eps 172.18.0.2)" '{"items":[{"spec":{}}]}')" '["172.18.0.2/32"]'
eq "kind and other nodes mixed -> not kind -> /32"       "$(derive '' "$(eps 172.18.0.2)" "$(nodes kind://docker/c/c-control-plane aws:///z/i-1)")" '["172.18.0.2/32"]'
eq "IPv6 endpoints are left out, as before"              "$(derive '' "$(eps fd00::1 10.0.0.5)" "$AWS")" '["10.0.0.5/32"]'

echo "# no address found"
eq "no endpoints is an error"                            "$(derive '' '{"items":[]}' "$KIND")" ERROR
eq "the error names the override"                        "$(derive '' '{"items":[]}' "$KIND" >/dev/null; printf '%s' "$ERR" | /usr/bin/grep -c KOURIER_APISERVER_CIDRS)" 1
eq "only IPv6 endpoints is an error"                     "$(derive '' "$(eps fd00::1)" "$AWS")" ERROR
eq "garbage JSON is an error"                            "$(derive '' 'not json' "$KIND")" ERROR

echo "# KOURIER_APISERVER_CIDRS overrides detection"
eq "override wins on kind"                               "$(derive '10.1.0.0/16' "$(eps 172.18.0.2)" "$KIND")" '["10.1.0.0/16"]'
eq "override wins elsewhere"                             "$(derive '192.168.8.0/24' "$(eps 10.0.0.5)" "$AWS")" '["192.168.8.0/24"]'
eq "comma separated"                                     "$(derive '10.1.0.0/16,10.2.0.0/16' '' '')" '["10.1.0.0/16","10.2.0.0/16"]'
eq "space separated"                                     "$(derive '10.2.0.0/16 10.1.0.0/16' '' '')" '["10.1.0.0/16","10.2.0.0/16"]'
eq "mixed separators, newlines, padding"                 "$(derive $' 10.1.0.0/16 ,\n10.2.0.0/16,, ' '' '')" '["10.1.0.0/16","10.2.0.0/16"]'
eq "a bare address is a /32"                             "$(derive '10.0.0.9' '' '')" '["10.0.0.9/32"]'
eq "a bare IPv6 address is a /128"                       "$(derive 'fd00::9' '' '')" '["fd00::9/128"]'
eq "an IPv6 CIDR"                                        "$(derive 'fd00::/64' '' '')" '["fd00::/64"]'
eq "duplicates collapse"                                 "$(derive '10.1.0.0/16 10.1.0.0/16' '' '')" '["10.1.0.0/16"]'
eq "the override needs no cluster data"                  "$(derive '10.1.0.0/16' 'not json' 'not json')" '["10.1.0.0/16"]'
eq "a prefix above 32 is refused"                        "$(derive '10.1.0.0/33' '' '')" ERROR
eq "a word is refused"                                   "$(derive 'anywhere' '' '')" ERROR
eq "one bad item refuses the lot"                        "$(derive '10.1.0.0/16 nope' '' '')" ERROR
eq "a non-numeric prefix is refused"      "$(derive '10.1.0.0/abc' '' '')" ERROR
eq "only separators is no override (detection runs)"     "$(derive ' , ' "$(eps 10.0.0.5)" "$AWS")" '["10.0.0.5/32"]'

echo "# kourier.sh install passes the derived CIDRs to helm"
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
mkdir "$T/bin"
for tool in jq yq; do ln -s "$(command -v "$tool")" "$T/bin/$tool"; done
cat > "$T/bin/kubectl" <<'FAKE'
#!/usr/bin/env bash
case " $* " in
  *" endpointslices.discovery.k8s.io "*) cat "$FIX_EPS" ;;
  *" nodes "*) cat "$FIX_NODES" ;;
  *) echo "fake kubectl: unexpected call: $*" >&2; exit 1 ;;
esac
FAKE
cat > "$T/bin/helm" <<'FAKE'
#!/usr/bin/env bash
case " $* " in
  *" list "*) echo '[]' ;;
  *" upgrade "*) while [ $# -gt 0 ]; do [ "$1" = -f ] && cp "$2" "$HELM_VALUES_OUT"; shift; done ;;
  *) echo "fake helm: unexpected call: $*" >&2; exit 1 ;;
esac
FAKE
chmod +x "$T/bin/kubectl" "$T/bin/helm"
# run_install <override> <eps json> <nodes json> -> the networkPolicy.kubeApiServer.cidrs helm received, or "ERROR"
run_install() {
  printf '%s' "$2" > "$T/eps.json"; printf '%s' "$3" > "$T/nodes.json"; rm -f "$T/values.out"
  PATH="$T/bin:/usr/bin:/bin" FIX_EPS="$T/eps.json" FIX_NODES="$T/nodes.json" HELM_VALUES_OUT="$T/values.out" \
    KOURIER_APISERVER_CIDRS="$1" KOURIER_TEAM_NS="openagentix-agents" KUBE_CONTEXT=fake bash "$HERE/kourier.sh" install >"$T/out.log" 2>&1 \
    && jq -c '.networkPolicy.kubeApiServer.cidrs' "$T/values.out" || echo ERROR
}
eq "kind: helm gets the /24"                             "$(run_install '' "$(eps 172.18.0.3)" "$KIND")" '["172.18.0.0/24"]'
eq "elsewhere: helm gets the /32"                        "$(run_install '' "$(eps 10.0.0.5)" "$AWS")" '["10.0.0.5/32"]'
eq "override: helm gets the override"                    "$(run_install '10.9.0.0/16' "$(eps 172.18.0.3)" "$KIND")" '["10.9.0.0/16"]'
eq "no address and no override: the install stops"       "$(run_install '' '{"items":[]}' "$KIND")" ERROR
run_install '' "$(eps 10.0.0.5)" "$AWS" >/dev/null
eq "the rest of the values are still derived"            "$(jq -c '[.config.connectionNamespaces, .networkPolicy.enabled]' "$T/values.out")" '[["openagentix-agents"],true]'

echo
echo "passed: $PASS  failed: $FAIL"
[ "$FAIL" -eq 0 ]
