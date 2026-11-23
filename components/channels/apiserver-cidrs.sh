#!/usr/bin/env bash
# The API server addresses Agent Kourier's NetworkPolicy lets it reach (chart value networkPolicy.kubeApiServer.cidrs).
# Sourced by kourier.sh; the test is tests/run.sh. Defines one function and no side effects, so it can be tested without a cluster.
#
#   kourier_apiserver_cidrs <override> <endpointslice-json> <nodes-json>
#
#   override            KOURIER_APISERVER_CIDRS: CIDRs (or bare IPs, taken as /32 or /128) separated by commas, spaces or
#                       newlines. When it is not empty it wins and the two JSON arguments are not read.
#   endpointslice-json  `kubectl get endpointslices -n default -l kubernetes.io/service-name=kubernetes -o json`
#   nodes-json          `kubectl get nodes -o json`
#
# Prints a JSON array of CIDRs (sorted, unique) and returns 0, or prints an error to stderr and returns 1.
#
# Without an override the CIDRs are derived from the IPv4 addresses of the `kubernetes` EndpointSlice, the API server's own:
#   - on kind (every node's spec.providerID starts with "kind://") each address is widened to its /24. The nodes are Docker
#     containers whose addresses come from the Docker network's pool and CHANGE when Docker restarts (172.18.0.2 became
#     172.18.0.3); a /32 then cuts Kourier off from the API server until the installer runs again. Docker's default pools give
#     the kind network a /16; a /24 keeps the rule inside the node network and covers the addresses a restart hands out to a
#     cluster of a few nodes. The rule only opens the API server port (the chart's port), to that /24.
#   - on any other cluster each address stays a /32: the endpoints of a real cluster are fixed, so least privilege applies.
kourier_apiserver_cidrs() {
  local override=${1:-} eps=${2:-} nodes=${3:-} list c
  if [ -n "$(printf '%s' "$override" | tr -d '[:space:],')" ]; then
    list=$(printf '%s' "$override" | tr -s '[:space:],' '[\n*]' | sed '/^$/d')
    while IFS= read -r c; do
      case $c in
        */*) ;;
        *:*) c="$c/128" ;;
        *) c="$c/32" ;;
      esac
      printf '%s\n' "$c"
    done <<<"$list" | jq -R . | jq -sc '
      if all(.[]; test("^([0-9]{1,3}[.]){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$") or test("^[0-9a-fA-F:]*:[0-9a-fA-F:.]*/([0-9]{1,2}|1[01][0-9]|12[0-8])$"))
      then unique else error("not a CIDR") end' 2>/dev/null \
      || { echo "ERROR: KOURIER_APISERVER_CIDRS must be IPv4 or IPv6 CIDRs (or bare addresses) separated by commas or spaces" >&2; return 1; }
    return 0
  fi
  jq -nc --argjson eps "${eps:-null}" --argjson nodes "${nodes:-null}" '
    ([($nodes.items // [])[].spec.providerID // ""] | (length > 0 and all(.[]; startswith("kind://")))) as $kind
    | [($eps.items // [])[].endpoints[]?.addresses[]? | select(test("^([0-9]{1,3}[.]){3}[0-9]{1,3}$"))]
    | map(if $kind then sub("[.][0-9]+$"; ".0/24") else . + "/32" end)
    | unique
    | if length == 0 then error("none") else . end' 2>/dev/null \
    || { echo "ERROR: could not find the API server address (endpointslice default/kubernetes); set KOURIER_APISERVER_CIDRS" >&2; return 1; }
}
