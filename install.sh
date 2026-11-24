#!/usr/bin/env bash
# OpenAgentix installer: one command, idempotent, ordered.
#   1. kind cluster (optional)            4. build/load the bridge and kb images, build/push the example skill
#   2. prerequisites (upstream Helm)      5. helm upgrade --install openagentix charts/openagentix
#   3. Secrets from the environment       6. ingest the sample knowledge corpus (when knowledge.enabled)
#
# Required env (unless the Secret already exists in the cluster):
#   ANTHROPIC_API_KEY       provider key for the claude-haiku model (-> agentgateway-system/anthropic-api-key)
#   TELEGRAM_BOT_TOKEN      bot token for the bridge             (-> openagentix-gateway/telegram-bridge)
# Optional env:
#   TELEGRAM_ALLOWED_CHAT_IDS  comma-separated chat ids allowed to talk to the agent (the bridge refuses to start without it, unless bridge.allowlist.enabled=false)
#   GOOGLE_API_KEY             creates agentgateway-system/gemini-api-key (for a gemini model you add to values)
#   NOTIFY_TELEGRAM_CHAT_ID    only with notify.enabled: creates (when absent, never printed, kept on re-runs) the Secret holding the
#                              destination `owner-telegram` (this chat) and copies the bridge's bot token next to it. This is the opt-in:
#                              installing never creates a way for agents to message someone without it.
#   NOTIFY_OWNER_TELEGRAM=1    the same, using the FIRST id of TELEGRAM_ALLOWED_CHAT_IDS when NOTIFY_TELEGRAM_CHAT_ID is not set
# Switches (all optional):
#   CLUSTER=openagentix  CREATE_CLUSTER=1  KUBE_CONTEXT=kind-$CLUSTER
#   SKIP_PREREQS=1       never touch kagent/Substrate/agentgateway/ToolHive
#   FORCE_PREREQS=1      re-run the upstream installers even if their Helm releases exist (this re-applies
#                        components/runtime/values/substrate.yaml and DROPS any Substrate additions made by spikes/ax)
#   SKIP_SKILL=1         do not build/push the example skill; use the digest in values.yaml
#   SKIP_BRIDGE_IMAGE=1  do not build/load the bridge image
#   SKIP_KB_IMAGE=1      do not build/load the knowledge base image (services/kb)
#   SKIP_A2A_CALL_IMAGE=1  do not build/load the schedule caller image (tools/a2a-call); it is only built when `schedules` is not empty
#   SKIP_KB_INGEST=1     do not ingest components/knowledge/sample into collection "openagentix"
#   SKIP_NOTIFY_IMAGE=1  do not build/load the notify image (services/notify); it is only built when notify.enabled is true
#   VALUES_FILE="a.yaml b.yaml"  extra values files for the chart, space-separated, applied in order (maps are merged, lists
#                        such as agents[] or schedules[] are REPLACED by the later file) (e.g. more agents, schedules, networkPolicy.extraA2ACallers for a
#                        chat gateway of your own). Helm replaces rather than merges the release values, so pass the
#                        same files on every run, or the extras disappear.
#   KOURIER_VALUES_FILE="k.yaml"  extra values for the Agent Kourier release (see components/channels/kourier-values.yaml)
#   KOURIER_APISERVER_CIDRS="10.0.0.0/24 ..."  the API server addresses Kourier's NetworkPolicy allows (comma or space separated); default: on kind the
#                        /24 of the API server's node address, elsewhere the exact endpoint addresses (components/channels/apiserver-cidrs.sh)
# Observability is opt-in too: the ONE switch is the chart value observability.enabled (components/observability/demo-values.yaml). When the chart
# renders its marker ConfigMap (openagentix-observability) this script applies the same settings to what the chart does not own: the kagent
# controller and agent runtimes (components/runtime/kagent-helm.sh, only when they differ from the release) and the Agent Kourier release.
# Channels and triggers (Agent Kourier) are opt-in. The ONE switch is the chart value channels.kourier.enabled (default false), set
# in a VALUES_FILE, e.g. VALUES_FILE=components/channels/demo-values.yaml ./install.sh . When it is true this script installs the
# pinned Kourier release (components/channels/kourier.sh, cosign-verified when cosign exists) BEFORE the chart, generates the
# Secret of each webhook connection when absent (random, never printed), and the chart admits Kourier's pod to the kagent controller.
#   RELEASE_NS=openagentix-system  AGENT_WAIT=900s
# Long-term memory (agents[].memory.enabled): needs pgvector in kagent's PostgreSQL; step 3b below enables it. The embedding
# ModelConfig's gateway key (Secret memory-virtual-key in the kagent namespace) is created like the agents' keys.
# The knowledge block creates (only when absent, never overwritten, so they stay stable across re-runs) the Secrets
# kb-admin, kb-postgres, kb-database and, for gateway-routed embeddings, kb-virtual-key (and, with knowledge.kb.graph.enabled, the extraction key kb-extract-key) in openagentix-knowledge.
# Secret values are never echoed, never put in argv or written to disk.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

CLUSTER=${CLUSTER:-openagentix}
CTX=${KUBE_CONTEXT:-kind-$CLUSTER}
NODE=${CLUSTER}-control-plane
K=${KUBECTL:-kubectl}
RELEASE=openagentix
RELEASE_NS=${RELEASE_NS:-openagentix-system}
CHART=charts/openagentix
AGENT_WAIT=${AGENT_WAIT:-900s}
CRANE_IMAGE=${CRANE_IMAGE:-gcr.io/go-containerregistry/crane:latest}
GW_NS=agentgateway-system
BRIDGE_NS=openagentix-gateway
BRIDGE_SECRET=telegram-bridge
KAGENT_NS=kagent

t0=$(date +%s)
log(){ printf '\n=== %s (+%ss)\n' "$*" "$(( $(date +%s) - t0 ))"; }
die(){ printf '\nERROR: %s\n' "$*" >&2; exit 1; }

# --- 0. preflight: tools and env, before anything is changed ----------------------------------------------
need=(docker kind kubectl helm jq yq openssl shasum)
missing=(); for c in "${need[@]}"; do command -v "$c" >/dev/null || missing+=("$c"); done
[ ${#missing[@]} -eq 0 ] || die "missing tools on PATH: ${missing[*]}"

CLUSTER_UP=0; kind get clusters 2>/dev/null | grep -qx "$CLUSTER" && CLUSTER_UP=1
secret_exists(){ [ "$CLUSTER_UP" = 1 ] && $K get secret -n "$1" "$2" >/dev/null 2>&1; }
problems=()
if [ -z "${ANTHROPIC_API_KEY:-}" ] && ! secret_exists $GW_NS anthropic-api-key; then
  problems+=("ANTHROPIC_API_KEY is not set and Secret $GW_NS/anthropic-api-key does not exist. Export it: export ANTHROPIC_API_KEY=sk-ant-...")
fi
if [ -z "${TELEGRAM_BOT_TOKEN:-}" ] && ! secret_exists $BRIDGE_NS $BRIDGE_SECRET; then
  problems+=("TELEGRAM_BOT_TOKEN is not set and Secret $BRIDGE_NS/$BRIDGE_SECRET does not exist. Create a bot with @BotFather, then: export TELEGRAM_BOT_TOKEN=...")
fi
if [ ${#problems[@]} -gt 0 ]; then
  printf '\nERROR: cannot start, required configuration is missing:\n' >&2
  for p in "${problems[@]}"; do printf '  - %s\n' "$p" >&2; done
  printf 'Nothing was changed.\n' >&2; exit 1
fi
[ -n "${TELEGRAM_ALLOWED_CHAT_IDS:-}" ] || echo "WARN: TELEGRAM_ALLOWED_CHAT_IDS not set. The bridge refuses to start without an allowlist (unless the Secret already has one, or you set bridge.allowlist.enabled=false in your values, which opens the bot to anyone)."

# --- 1. kind cluster ------------------------------------------------------------------------------------
log "cluster $CLUSTER"
if [ "$CLUSTER_UP" = 0 ]; then
  [ "${CREATE_CLUSTER:-1}" = 1 ] || die "kind cluster '$CLUSTER' does not exist and CREATE_CLUSTER=0"
  kind create cluster --config hack/kind-config.yaml
fi
$K config use-context "$CTX" >/dev/null || die "kube context $CTX not found"
docker exec "$NODE" sysctl -w net.ipv4.conf.all.proxy_arp=1 >/dev/null   # gVisor pod networking (see components/runtime/NOTES.md)
docker exec "$NODE" sysctl -e -w net.ipv6.conf.all.proxy_ndp=1 >/dev/null

# --- 2. prerequisites: reuse the component scripts, pinned versions live in those scripts ---------------------
helm_has(){ helm status "$1" -n "$2" >/dev/null 2>&1; }
want(){ [ "${SKIP_PREREQS:-0}" != 1 ] && { [ "${FORCE_PREREQS:-0}" = 1 ] || ! "$@"; }; }
log "prerequisites"
if want bash -c "$(declare -f helm_has); helm_has kagent $KAGENT_NS && helm_has substrate ate-system"; then
  echo "-> kagent 1.0.1 + Agent Substrate 1.0.1 (components/runtime/up.sh)"
  SKIP_FIRST_AGENT=1 bash components/runtime/up.sh
else echo "kagent + Substrate: present or skipped"; fi
if want bash -c "$(declare -f helm_has); helm_has agentgateway $GW_NS"; then
  echo "-> Gateway API CRDs + agentgateway v1.7.0 (components/model-gateway/install.sh)"
  PREREQS_ONLY=1 bash components/model-gateway/install.sh
else echo "agentgateway: present or skipped"; fi
if want bash -c "$(declare -f helm_has); helm_has toolhive-operator toolhive-system"; then
  echo "-> ToolHive 0.67.1 (components/tools/install.sh)"
  PREREQS_ONLY=1 bash components/tools/install.sh
else echo "ToolHive: present or skipped"; fi
$K config use-context "$CTX" >/dev/null

# --- 3. what will the chart render? (agents, namespaces, images) -------------------------------------------
VALS=(); for f in ${VALUES_FILE:-}; do VALS+=(-f "$f"); done
# --api-versions: the Agent Kourier CRDs are installed later in this run, but the objects they define must be planned now.
render(){ helm template $RELEASE $CHART -n "$RELEASE_NS" --api-versions agentkourier.dev/v1alpha1 ${VALS[@]+"${VALS[@]}"} "$@" | yq -c 'select(.!=null)'; }
RENDERED=$(render) || die "chart does not render; run: helm template $RELEASE $CHART ${VALUES_FILE:+-f $VALUES_FILE}"
AGENT_KEYS=$(jq -r 'select(.kind=="ModelConfig")|[.metadata.annotations["openagentix.io/agent"],.spec.apiKeySecret,.metadata.namespace]|@tsv' <<<"$RENDERED")
NAMESPACES=$(jq -r 'select(.metadata.namespace!=null)|.metadata.namespace' <<<"$RENDERED" | sort -u)
BRIDGE_IMAGE=$(jq -r 'select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].image' <<<"$RENDERED")
KB_DEP=$(jq -c 'select(.kind=="Deployment" and .metadata.name=="kb")' <<<"$RENDERED")   # empty when knowledge.enabled=false
# Consumers other than agents that need their own gateway virtual key (the kb, for embeddings): "user<TAB>secret<TAB>ns"
NOTIFY_DEP=$(jq -c 'select(.kind=="Deployment" and .metadata.name=="notify")' <<<"$RENDERED")   # empty when notify.enabled=false
# A Deployment asks for one key per annotation SET: openagentix.io/gateway-user<sfx> (the user id), gateway-key-secret<sfx> (the Secret) and,
# optionally, gateway-key-name<sfx> (the Secret key, default OPENAI_API_KEY); <sfx> is empty for the first set, "-extract" for the kb's
# extraction key. Annotation names are unique per object, so a second key on one Deployment needs a second suffix: the earlier one-pair lookup
# is the same expression with an empty suffix.
KB_KEY=$(jq -r 'select(.kind=="Deployment")|.metadata as $m|($m.annotations//{})|to_entries[]|select(.key|test("^openagentix.io/gateway-user(-.+)?$"))|select(.value!="")|(.key|ltrimstr("openagentix.io/gateway-user")) as $sfx|[.value,$m.annotations["openagentix.io/gateway-key-secret"+$sfx],$m.namespace,($m.annotations["openagentix.io/gateway-key-name"+$sfx]//"OPENAI_API_KEY")]|@tsv' <<<"$RENDERED")
[ -z "$KB_KEY" ] || AGENT_KEYS=$(printf '%s\n%s' "$AGENT_KEYS" "$KB_KEY")

# --- 3b. long-term memory needs pgvector in kagent's PostgreSQL and config.database.vectorEnabled=true ------------------
# Only when some agent opts in (Harness spec.kagent.memory). Fresh installs get both from components/runtime/up.sh; this step
# brings an existing cluster up to date with the SAME scripts (pgvector.sh is idempotent, kagent-helm.sh is the one
# helm command that always carries values/kagent.yaml). It never touches kagent data. SKIP_PREREQS=1 skips it.
if [ "${SKIP_PREREQS:-0}" != 1 ] && jq -e -s 'any(.[]; .kind=="Harness" and (.spec.kagent.memory!=null))' <<<"$RENDERED" >/dev/null; then
  log "kagent long-term memory prerequisites (pgvector)"
  bash components/runtime/pgvector.sh
  if [ "$(helm get values kagent -n $KAGENT_NS -a -o json | jq -r '.config.database.vectorEnabled // false')" = true ]; then
    echo "kagent release: config.database.vectorEnabled already true"
  else
    echo "-> enabling config.database.vectorEnabled (components/runtime/kagent-helm.sh)"
    bash components/runtime/kagent-helm.sh
  fi
fi

# --- 3c. observability: kagent controller + agent runtimes, from the chart's marker ConfigMap ----------------------------------
# Desired state is computed from the same values the chart used (one switch). The kagent release is only touched when its
# telemetry settings differ from that state, in either direction (off removes them), so a re-run changes nothing.
OBS_CM=$(jq -c 'select(.kind=="ConfigMap" and .metadata.name=="openagentix-observability")' <<<"$RENDERED")
OBS_KOURIER_FILE=""
if [ "${SKIP_PREREQS:-0}" != 1 ] && helm_has kagent $KAGENT_NS; then
  if [ -n "$OBS_CM" ]; then
    O_EP=$(jq -r '.data.otlpEndpoint' <<<"$OBS_CM"); O_PROTO=$(jq -r '.data.otlpProtocol' <<<"$OBS_CM")
    O_TRACES=$(jq -r '.data.runtimeTraces' <<<"$OBS_CM"); O_CAP=$(jq -r '.data.captureContent' <<<"$OBS_CM"); O_BAG=$(jq -r '.data.baggage' <<<"$OBS_CM")
    O_PROP=$(jq -r '.data.propagators' <<<"$OBS_CM")
    # kagent: an http/protobuf FILE endpoint carries the signal path; grpc takes host:port as is.
    if [ "$O_PROTO" = grpc ]; then O_FILE_EP="$O_EP"; else O_FILE_EP="${O_EP%/}/v1/traces"; fi
    OBS_WANT=$(jq -nc --arg ep "$O_FILE_EP" --arg proto "$O_PROTO" --argjson on "$O_TRACES" --argjson cap "$O_CAP" --argjson bag "$O_BAG" --arg prop "$O_PROP" '
      if $on then {config:{telemetry:{traces:{endpoint:$ep,protocol:$proto}},
                           runtimeDefaults:{telemetry:{traces:{endpoint:$ep,protocol:$proto},captureContent:$cap}}}}
                  + (if $bag then {controller:{env:[{name:"OTEL_PROPAGATORS",value:$prop}]}} else {} end)
      else {} end')
  else
    OBS_WANT='{}'
  fi
  OBS_NORM='{t:(.config.telemetry//{}), r:(.config.runtimeDefaults.telemetry//{}), e:([.controller.env[]?|select(.name=="OTEL_PROPAGATORS")])}'
  OBS_HAVE=$(helm get values kagent -n $KAGENT_NS -o json | jq -cS "$OBS_NORM")
  OBS_WANT_N=$(jq -cS "$OBS_NORM" <<<"$OBS_WANT")
  if [ "$OBS_HAVE" = "$OBS_WANT_N" ]; then
    echo "kagent release: telemetry settings already as wanted"
  else
    log "kagent telemetry settings (components/runtime/kagent-helm.sh)"
    OBS_T=$(mktemp -d)
    jq -c . <<<"$OBS_WANT" > "$OBS_T/telemetry.json"
    if [ "$OBS_WANT" = '{}' ]; then bash components/runtime/kagent-helm.sh; else KAGENT_EXTRA_VALUES="$OBS_T/telemetry.json" bash components/runtime/kagent-helm.sh; fi
    rm -rf "$OBS_T"
  fi
fi
if [ -n "$OBS_CM" ]; then   # Agent Kourier: OTLP env and who may scrape / be reached (merged into its release values by kourier.sh)
  OBS_T=$(mktemp -d)
  jq -n --argjson env "$(jq -c '.data.kourierEnv|fromjson' <<<"$OBS_CM")" --argjson from "$(jq -c '.data.kourierMetricsFrom|fromjson' <<<"$OBS_CM")" --argjson eg "$(jq -c '.data.kourierEgress|fromjson' <<<"$OBS_CM")" \
    '(if ($env|length)>0 then {extraEnv:$env} else {} end) + (if ($from|length)>0 then {networkPolicy:{metricsFrom:$from}} else {} end)' > "$OBS_T/kourier-obs.json"
  # extraEgress is set by kourier.sh from the chart (KOURIER_EGRESS); add the OTLP rule to that same list.
  OBS_KOURIER_EGRESS=$(jq -c '.data.kourierEgress|fromjson' <<<"$OBS_CM")
  OBS_KOURIER_FILE="$OBS_T/kourier-obs.json"
fi

for ns in $RELEASE_NS $NAMESPACES; do
  $K get namespace "$ns" >/dev/null 2>&1 || $K create namespace "$ns" >/dev/null
done

# --- 4. secrets (values from env only) ------------------------------------------------------------------------
# put_secret ns name key ENVVAR: server-side apply with one field manager per key, so keys do not clobber each
# other and no last-applied annotation (which would contain the value) is written. Skipped when ENVVAR is unset.
put_secret(){
  local ns=$1 name=$2 key=$3 var=$4
  [ -n "${!var:-}" ] || return 0
  $K create secret generic "$name" -n "$ns" --from-file="$key"=<(printf '%s' "${!var}") --dry-run=client -o yaml \
    | $K apply --server-side --force-conflicts --field-manager="openagentix-$key" -f - >/dev/null
  echo "secret $ns/$name key $key applied from \$$var"
}
log "secrets"
put_secret $GW_NS anthropic-api-key Authorization ANTHROPIC_API_KEY    # key name must be 'Authorization'
put_secret $GW_NS gemini-api-key Authorization GOOGLE_API_KEY
put_secret $BRIDGE_NS $BRIDGE_SECRET TELEGRAM_BOT_TOKEN TELEGRAM_BOT_TOKEN
put_secret $BRIDGE_NS $BRIDGE_SECRET TELEGRAM_ALLOWED_CHAT_IDS TELEGRAM_ALLOWED_CHAT_IDS

mk_random_secret(){  # ns name key [bytes]: created only if absent, value from openssl, never printed
  $K get secret -n "$1" "$2" >/dev/null 2>&1 && { echo "secret $1/$2: kept"; return 0; }
  $K create secret generic "$2" -n "$1" --from-file="$3"=<(printf '%s' "$(openssl rand -hex "${4:-24}")") >/dev/null && echo "secret $1/$2 key $3: generated"
}

# Knowledge base Secrets: each is created only if absent (so re-runs never rotate a password or token), values come
# from openssl and are never printed. kb-database is built from the kb-postgres password.
if [ -n "$KB_DEP" ]; then
  KB_NS=$(jq -r '.metadata.namespace' <<<"$KB_DEP")
  kb_secret_ref(){ jq -r --arg v "$1" '.spec.template.spec.containers[0].env[]|select(.name==$v)|.valueFrom.secretKeyRef|[.name,.key]|@tsv' <<<"$KB_DEP"; }
  IFS=$'\t' read -r ADM_S ADM_K <<<"$(kb_secret_ref KB_ADMIN_TOKEN)"
  IFS=$'\t' read -r DB_S DB_K <<<"$(kb_secret_ref KB_DATABASE_URL)"
  mk_random_secret "$KB_NS" "$ADM_S" "$ADM_K"
  PG_SS=$(jq -c 'select(.kind=="StatefulSet" and .metadata.name=="kb-postgres")' <<<"$RENDERED")
  if [ -n "$PG_SS" ]; then
    PG_S=$(jq -r '.spec.template.spec.containers[0].env[]|select(.name=="POSTGRES_PASSWORD")|.valueFrom.secretKeyRef.name' <<<"$PG_SS")
    PG_K=$(jq -r '.spec.template.spec.containers[0].env[]|select(.name=="POSTGRES_PASSWORD")|.valueFrom.secretKeyRef.key' <<<"$PG_SS")
    PG_U=$(jq -r '.spec.template.spec.containers[0].env[]|select(.name=="POSTGRES_USER")|.value' <<<"$PG_SS")
    PG_D=$(jq -r '.spec.template.spec.containers[0].env[]|select(.name=="POSTGRES_DB")|.value' <<<"$PG_SS")
    mk_random_secret "$KB_NS" "$PG_S" "$PG_K"
    if $K get secret -n "$KB_NS" "$DB_S" >/dev/null 2>&1; then echo "secret $KB_NS/$DB_S: kept"; else
      PGPW=$($K get secret -n "$KB_NS" "$PG_S" -o jsonpath="{.data.$PG_K}" | base64 -d)
      $K create secret generic "$DB_S" -n "$KB_NS" --from-file="$DB_K"=<(printf 'postgres://%s:%s@kb-postgres.%s.svc.cluster.local:5432/%s?sslmode=disable' "$PG_U" "$PGPW" "$KB_NS" "$PG_D") >/dev/null
      unset PGPW; echo "secret $KB_NS/$DB_S key $DB_K: generated"
    fi
  else
    $K get secret -n "$KB_NS" "$DB_S" >/dev/null 2>&1 || echo "WARN: knowledge.postgres.enabled=false and Secret $KB_NS/$DB_S does not exist: create it with the URL of your PostgreSQL (pgvector)"
  fi
fi

# One virtual key per agent (and per other gateway consumer, the kb), so gateway logs/metrics/budgets are attributed per agent. The raw key lives only in
# Secret <ns>/<agent>-virtual-key; the gateway ConfigMap llm-api-keys holds SHA-256 hashes.
NOTIFY_NO_DEST=0
if [ -n "$NOTIFY_DEP" ]; then
  log "notify destinations"
  N_NS=$(jq -r '.metadata.namespace' <<<"$NOTIFY_DEP")
  N_DEST_S=$(jq -r '.spec.template.spec.volumes[]|select(.name=="destinations")|.secret.secretName' <<<"$NOTIFY_DEP")
  N_DEST_K=$(jq -r '.spec.template.spec.volumes[]|select(.name=="destinations")|.secret.items[0].key' <<<"$NOTIFY_DEP")
  IFS=$'\t' read -r N_TOK_S N_TOK_K <<<"$(jq -r '.spec.template.spec.containers[0].env[]|select(.name=="NOTIFY_TELEGRAM_TOKEN")|.valueFrom.secretKeyRef|[.name,.key]|@tsv' <<<"$NOTIFY_DEP")"
  # The owner's chat id is personal data: it goes only into the destinations Secret, from the environment, never to output or files.
  # Creating a way for agents to message someone is deliberate: an explicit NOTIFY_TELEGRAM_CHAT_ID, or NOTIFY_OWNER_TELEGRAM=1
  # (use the first id of TELEGRAM_ALLOWED_CHAT_IDS). Without either, nothing is created.
  N_CREATED=0
  if $K get secret -n "$N_NS" "$N_DEST_S" >/dev/null 2>&1; then
    echo "secret $N_NS/$N_DEST_S: kept"
  else
    N_CHAT=${NOTIFY_TELEGRAM_CHAT_ID:-}
    if [ -z "$N_CHAT" ] && [ "${NOTIFY_OWNER_TELEGRAM:-0}" = 1 ]; then
      N_CHAT=${TELEGRAM_ALLOWED_CHAT_IDS:-$($K get secret -n $BRIDGE_NS $BRIDGE_SECRET -o jsonpath='{.data.TELEGRAM_ALLOWED_CHAT_IDS}' 2>/dev/null | base64 -d 2>/dev/null || true)}
      N_CHAT=${N_CHAT%%,*}; N_CHAT=${N_CHAT// /}
    fi
    if [ -z "$N_CHAT" ]; then
      echo "notify: no destination configured (set NOTIFY_TELEGRAM_CHAT_ID, or NOTIFY_OWNER_TELEGRAM=1 to use the first TELEGRAM_ALLOWED_CHAT_IDS entry). The service runs and refuses every notify call."
    elif [ -z "$N_TOK_S" ]; then
      echo "WARN: notify has no NOTIFY_TELEGRAM_TOKEN env var in these values (notify.tokens): not creating the owner-telegram destination"
    else
      [[ "$N_CHAT" =~ ^-?[0-9]+$ ]] || die "the notify chat id must be a number (NOTIFY_TELEGRAM_CHAT_ID)"
      $K create secret generic "$N_DEST_S" -n "$N_NS" --from-file="$N_DEST_K"=<(printf 'destinations:\n  - name: owner-telegram\n    description: "The platform owner'"'"'s Telegram chat"\n    platform: telegram\n    chatId: "%s"\n    tokenEnv: NOTIFY_TELEGRAM_TOKEN\n' "$N_CHAT") >/dev/null \
        && { echo "secret $N_NS/$N_DEST_S key $N_DEST_K: created with destination owner-telegram"; N_CREATED=1; }
    fi
    unset N_CHAT
  fi
  # The bot token is copied (a Secret cannot be mounted across namespaces) when this run created the destination, or when an
  # existing destinations file has owner-telegram and the copy is missing. Never printed.
  N_DEST_YAML=$($K get secret -n "$N_NS" "$N_DEST_S" -o json 2>/dev/null | jq -r --arg k "$N_DEST_K" '.data[$k]//""' | base64 -d 2>/dev/null || true)
  if [ -n "$N_TOK_S" ] && ! $K get secret -n "$N_NS" "$N_TOK_S" >/dev/null 2>&1 \
     && { [ "$N_CREATED" = 1 ] || yq -e '.destinations[]|select(.name=="owner-telegram")' >/dev/null 2>&1 <<<"$N_DEST_YAML"; }; then
    N_TOKEN=$($K get secret -n $BRIDGE_NS $BRIDGE_SECRET -o jsonpath='{.data.TELEGRAM_BOT_TOKEN}' | base64 -d)
    $K create secret generic "$N_TOK_S" -n "$N_NS" --from-file="$N_TOK_K"=<(printf '%s' "$N_TOKEN") >/dev/null && echo "secret $N_NS/$N_TOK_S key $N_TOK_K: copied from $BRIDGE_NS/$BRIDGE_SECRET"
    unset N_TOKEN
  fi
  [ "$(yq '.destinations|length' <<<"${N_DEST_YAML:-}" 2>/dev/null || echo 0)" -gt 0 ] 2>/dev/null || NOTIFY_NO_DEST=1
  unset N_DEST_YAML
fi

CM_ARGS=()
while IFS=$'\t' read -r agent secret ns vkey; do
  [ -n "$agent" ] || continue
  vkey=${vkey:-OPENAI_API_KEY}
  $K get secret -n "$ns" "$secret" >/dev/null 2>&1 || \
    $K create secret generic "$secret" -n "$ns" --from-file="$vkey"=<(printf 'vk-%s' "$(openssl rand -hex 16)") >/dev/null
  h=$($K get secret -n "$ns" "$secret" -o jsonpath="{.data.${vkey//./\\.}}" | base64 -d | shasum -a 256 | awk '{print "sha256:"$1}')
  CM_ARGS+=(--from-literal="$agent={\"keyHash\":\"$h\",\"metadata\":{\"user_id\":\"$agent\"}}")
  echo "virtual key for agent '$agent': Secret $ns/$secret"
done <<<"$AGENT_KEYS"
if [ ${#CM_ARGS[@]} -gt 0 ]; then
  $K create configmap llm-api-keys -n $GW_NS "${CM_ARGS[@]}" --dry-run=client -o yaml \
    | $K label --local -f - agentgateway.dev/apikey=true -o yaml | $K apply -f - >/dev/null
  echo "ConfigMap $GW_NS/llm-api-keys: ${#CM_ARGS[@]} hashed key(s)"
fi

# --- 4b. trust anchor for the vMCP's OIDC validation -------------------------------------------------------------
# The vMCP verifies Substrate actor JWTs against https://idp.ate-system.svc, whose TLS certificate is signed by
# Substrate's service-dns CA. Copy that CA (public certificate, not a key) into the ConfigMap the chart references.
CA_REF=$(jq -r 'select(.kind=="MCPOIDCConfig")|.spec.inline.caBundleRef.configMapRef|[.name,.key]|@tsv' <<<"$RENDERED" | head -1)
if [ -n "$CA_REF" ]; then
  log "IdP CA bundle for the vMCP"
  IFS=$'\t' read -r CA_CM CA_KEY <<<"$CA_REF"
  CA_NS=$(jq -r 'select(.kind=="MCPOIDCConfig")|.metadata.namespace' <<<"$RENDERED" | head -1)
  CTB=${IDP_CTB:-servicedns.podcert.ate.dev:identity:primary-bundle}
  CA_PEM=$($K get clustertrustbundle "$CTB" -o jsonpath='{.spec.trustBundle}' 2>/dev/null) || CA_PEM=""
  [ -n "$CA_PEM" ] || die "ClusterTrustBundle $CTB not found or empty (is Substrate installed?). Set mcp.virtual.incomingAuth.type=anonymous to skip OIDC."
  $K create configmap "$CA_CM" -n "$CA_NS" --from-file="$CA_KEY"=<(printf '%s\n' "$CA_PEM") --dry-run=client -o yaml | $K apply -f - >/dev/null
  echo "ConfigMap $CA_NS/$CA_CM key $CA_KEY from ClusterTrustBundle $CTB"
fi

# --- 4c. channels and triggers (Agent Kourier), only when the chart renders the marker (channels.kourier.enabled) ------------
CH_CM=$(jq -c 'select(.kind=="ConfigMap" and .metadata.name=="openagentix-channels")' <<<"$RENDERED")
if [ -n "$CH_CM" ]; then
  log "channels and triggers (Agent Kourier)"
  CH_NS=$(jq -r '.data.namespace' <<<"$CH_CM"); CH_BROKER_NS=$(jq -r '.data.brokerNamespace' <<<"$CH_CM")
  # Webhook connections: the Secret key follows the verify scheme (hmac-sha256: webhookSecret, token: token). Slack Secrets hold the
  # operator's own tokens: only checked, never created here.
  while IFS=$'\t' read -r cplat csec cscheme; do
    [ -n "$csec" ] || continue
    if [ "$cplat" = webhook ]; then
      case "$cscheme" in token) ckey=token;; *) ckey=webhookSecret;; esac
      mk_random_secret "$CH_NS" "$csec" "$ckey" 32
    else
      $K get secret -n "$CH_NS" "$csec" >/dev/null 2>&1 || echo "WARN: Secret $CH_NS/$csec for the $cplat connection does not exist: create it first (README \"Channels and triggers\", keys botToken and appToken)"
    fi
  done < <(jq -r 'select(.kind=="ChatConnection")|[.spec.platform,.spec.credentialsSecretRef.name,(.spec.webhook.verify.scheme//"")]|@tsv' <<<"$RENDERED")
  KOURIER_EGRESS_ALL=$(jq -c --argjson more "${OBS_KOURIER_EGRESS:-[]}" '.data.brokerEgress|fromjson + $more' <<<"$CH_CM")
  KOURIER_TEAM_NS="$CH_NS" KOURIER_NS="$CH_BROKER_NS" KOURIER_EGRESS="$KOURIER_EGRESS_ALL" KOURIER_VALUES_FILE="${KOURIER_VALUES_FILE:-}${OBS_KOURIER_FILE:+ $OBS_KOURIER_FILE}" \
    CLUSTER="$CLUSTER" KUBE_CONTEXT="$CTX" KUBECTL="$K" bash components/channels/kourier.sh install
elif helm status agent-kourier -n agent-kourier >/dev/null 2>&1; then
  echo "NOTE: Agent Kourier is installed but channels.kourier.enabled is false in these values: this run removes its connections and bindings. Pass the same VALUES_FILE as before to keep them, or ./uninstall.sh to remove Kourier."
fi

[ -z "$OBS_KOURIER_FILE" ] || rm -rf "$(dirname "$OBS_KOURIER_FILE")"

# --- 5. images ----------------------------------------------------------------------------------------------
SET=()
if [ "${SKIP_BRIDGE_IMAGE:-0}" != 1 ] && [[ "$BRIDGE_IMAGE" == openagentix/telegram-bridge:* ]]; then
  log "bridge image $BRIDGE_IMAGE"
  docker build -q -t "$BRIDGE_IMAGE" bridges/telegram >/dev/null
  kind load docker-image "$BRIDGE_IMAGE" --name "$CLUSTER"
fi
# Schedules: the CronJobs run tools/a2a-call. The image is built only when the chart renders a CronJob for it (empty `schedules` = no
# build, no namespace, no pods), then keyed on the source hash like the bridge and the kb.
A2A_CALL_IMAGE=$(jq -r 'select(.kind=="CronJob")|.spec.jobTemplate.spec.template.spec.containers[0].image' <<<"$RENDERED" | sort -u | head -1)
if [ "${SKIP_A2A_CALL_IMAGE:-0}" != 1 ] && [[ "$A2A_CALL_IMAGE" == openagentix/a2a-call:* ]]; then
  log "a2a-call image $A2A_CALL_IMAGE (schedules)"
  docker build -q -t "$A2A_CALL_IMAGE" tools/a2a-call >/dev/null
  kind load docker-image "$A2A_CALL_IMAGE" --name "$CLUSTER"
fi
KB_IMAGE=$(jq -r '.spec.template.spec.containers[0].image' <<<"${KB_DEP:-null}" 2>/dev/null || true)
if [ "${SKIP_KB_IMAGE:-0}" != 1 ] && [[ "$KB_IMAGE" == openagentix/kb:* ]]; then
  log "kb image $KB_IMAGE"
  docker build -q -t "$KB_IMAGE" services/kb >/dev/null
  kind load docker-image "$KB_IMAGE" --name "$CLUSTER"
fi
NOTIFY_IMAGE=$(jq -r '.spec.template.spec.containers[0].image' <<<"${NOTIFY_DEP:-null}" 2>/dev/null || true)
if [ "${SKIP_NOTIFY_IMAGE:-0}" != 1 ] && [[ "$NOTIFY_IMAGE" == openagentix/notify:* ]]; then
  log "notify image $NOTIFY_IMAGE"
  docker build -q -t "$NOTIFY_IMAGE" services/notify >/dev/null
  kind load docker-image "$NOTIFY_IMAGE" --name "$CLUSTER"
fi
if [ -n "$BRIDGE_IMAGE" ]; then
  # Same for the bridge (image tag "dev"): the pod template carries a hash of bridges/telegram, so changed code replaces the pod.
  BRIDGE_HASH=$(cd bridges/telegram && find . -type f ! -path './bin/*' ! -name '*.md' ! -name '*.test' ! -name 'coverage*' -print0 | LC_ALL=C sort -z | xargs -0 shasum -a 256 | shasum -a 256 | cut -c1-16)
  SET+=(--set-string "bridge.sourceHash=$BRIDGE_HASH")
fi
if [ -n "$A2A_CALL_IMAGE" ]; then
  A2A_HASH=$(cd tools/a2a-call && find . -type f ! -path './bin/*' ! -name '*.md' ! -name '*.test' ! -name 'coverage*' -print0 | LC_ALL=C sort -z | xargs -0 shasum -a 256 | shasum -a 256 | cut -c1-16)
  SET+=(--set-string "scheduling.sourceHash=$A2A_HASH")
fi
if [ -n "$KB_DEP" ]; then
  # The image tag is "dev" and image ids change with every build, so key the rollout on the source content instead:
  # unchanged code = identical pod template = no restart; changed code = new hash = the pod is replaced.
  KB_HASH=$(cd services/kb && find . -type f ! -path './bin/*' ! -name '*.test' ! -name 'coverage*' -print0 | LC_ALL=C sort -z | xargs -0 shasum -a 256 | shasum -a 256 | cut -c1-16)
  SET+=(--set-string "knowledge.kb.sourceHash=$KB_HASH")
fi
if [ -n "$NOTIFY_DEP" ]; then
  # Same for notify: a hash of services/notify rolls the pod on a code change although the tag stays "dev".
  NOTIFY_HASH=$(cd services/notify && find . -type f ! -path './bin/*' ! -name '*.md' ! -name '*.test' ! -name '*_test.go' ! -name 'coverage*' -print0 | LC_ALL=C sort -z | xargs -0 shasum -a 256 | shasum -a 256 | cut -c1-16)
  SET+=(--set-string "notify.sourceHash=$NOTIFY_HASH")
  # No destination yet: the service is not Ready by design (it fails closed), and `helm --wait` must not wait for that.
  [ "$NOTIFY_NO_DEST" = 0 ] || SET+=(--set notify.requireReady=false)
fi
if [ "${SKIP_SKILL:-0}" != 1 ] && jq -e 'select(.kind=="AgentTemplate")|.spec.skills[]?|select(.source.oci|test("/skills/openagentix-canary@"))' <<<"$RENDERED" >/dev/null; then
  log "example skill openagentix-canary"
  # A registry:2 container of our own on the kind docker network (NOT in the node's network namespace: that dies
  # with the node and breaks on cluster recreation). The node pulls from its IP over plain HTTP (RFC1918 address).
  # The IP is looked up on every run and written into the skill ref, so nothing hard-codes it.
  REGISTRY=${REGISTRY_NAME:-openagentix-registry}
  if ! docker inspect "$REGISTRY" >/dev/null 2>&1; then
    docker run -d --name "$REGISTRY" --restart unless-stopped --network kind registry:2 >/dev/null
  else
    [ "$(docker inspect -f '{{.State.Running}}' "$REGISTRY")" = true ] || docker start "$REGISTRY" >/dev/null
    docker inspect -f '{{json .NetworkSettings.Networks}}' "$REGISTRY" | jq -e 'has("kind")' >/dev/null || docker network connect kind "$REGISTRY"
  fi
  REG_IP=""
  for _ in $(seq 1 30); do
    REG_IP=$(docker inspect -f '{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}' "$REGISTRY")
    [ -n "$REG_IP" ] && break; sleep 1
  done
  [ -n "$REG_IP" ] || die "registry container $REGISTRY has no address on the kind network"
  REG="$REG_IP:5000"
  ARCH=$(docker exec "$NODE" uname -m); case "$ARCH" in aarch64) ARCH=arm64;; x86_64) ARCH=amd64;; esac
  T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
  docker build -q --platform "linux/$ARCH" -t openagentix-canary-skill:1.0.0 -f components/tools/skills/Dockerfile components/tools/skills >/dev/null
  docker save -o "$T/skill.tar" openagentix-canary-skill:1.0.0
  DIGEST=$(docker run --rm --network kind -v "$T:/w" "$CRANE_IMAGE" push /w/skill.tar "$REG/skills/openagentix-canary:1.0.0" --insecure 2>&1 \
    | grep -Eo 'sha256:[0-9a-f]{64}' | tail -1)
  [ -n "$DIGEST" ] || die "could not push the skill image to $REG (is $REGISTRY reachable on the kind docker network?)"
  SET+=(--set-string "skills.openagentix-canary.oci=$REG/skills/openagentix-canary@$DIGEST")
  echo "skill pinned at $REG/skills/openagentix-canary@$DIGEST"
fi

# --- 6a. adopt hand-applied objects the chart now owns (label + annotations; nothing is deleted or recreated) ----
adopted=0
while IFS=$'\t' read -r res name ns; do
  [ -n "$res" ] || continue
  $K get "$res" "$name" -n "$ns" >/dev/null 2>&1 || continue
  [ "$($K get "$res" "$name" -n "$ns" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}')" = "$RELEASE" ] && continue
  $K label "$res" "$name" -n "$ns" app.kubernetes.io/managed-by=Helm --overwrite >/dev/null
  $K annotate "$res" "$name" -n "$ns" meta.helm.sh/release-name=$RELEASE meta.helm.sh/release-namespace="$RELEASE_NS" --overwrite >/dev/null
  echo "adopted $res/$name ($ns)"; adopted=$((adopted+1))
done < <(jq -r 'select(.metadata.namespace!=null)|[((.kind|ascii_downcase)+(if (.apiVersion|contains("/")) then "."+(.apiVersion|split("/")[0]) else "" end)),.metadata.name,.metadata.namespace]|@tsv' <<<"$RENDERED")

# --- 6. the chart ---------------------------------------------------------------------------------------------
log "helm upgrade --install $RELEASE"  # (adoption happens just above)
helm upgrade --install $RELEASE $CHART -n "$RELEASE_NS" --create-namespace ${VALS[@]+"${VALS[@]}"} ${SET[@]+"${SET[@]}"} --wait --timeout 5m

if [ -n "$CH_CM" ]; then   # the broker's CRDs must have been there: the chart skips Kourier objects silently without them
  helm get manifest $RELEASE -n "$RELEASE_NS" | yq -e 'select(.kind=="AgentBackend")' >/dev/null || die "Agent Kourier CRDs were not found by Helm: the AgentBackend was not installed (kubectl get crd agentbackends.agentkourier.dev)"
fi

# Helm does not patch objects it merely adopted when old and new manifests are equal, so converge them once with
# server-side apply (no-op for objects Helm just created).
helm get manifest $RELEASE -n "$RELEASE_NS" | $K apply --server-side --force-conflicts --field-manager=openagentix-install -f - >/dev/null
[ "$adopted" -eq 0 ] || echo "reconciled $adopted adopted object(s) to the chart's desired state"

# --- 6b. knowledge base: wait, then ingest the sample corpus (idempotent PUTs, token read from the Secret) -------
if [ -n "$KB_DEP" ]; then
  log "knowledge base"
  for w in $(jq -r 'select((.kind=="Deployment" or .kind=="StatefulSet") and .metadata.namespace=="'"$KB_NS"'")|.kind+"/"+.metadata.name' <<<"$RENDERED"); do
    $K rollout status "$w" -n "$KB_NS" --timeout=600s
  done
  if [ "${SKIP_KB_INGEST:-0}" != 1 ]; then
    KUBECTL=$K KUBE_CONTEXT=$CTX KB_NAMESPACE=$KB_NS KB_ADMIN_SECRET=$ADM_S KB_ADMIN_KEY=$ADM_K bash hack/kb-ingest.sh components/knowledge/sample openagentix
  fi
fi

# --- 7. wait for the agents ---------------------------------------------------------------------------------
if [ -n "$NOTIFY_DEP" ]; then
  log "notify"
  $K rollout status deployment/notify -n "$N_NS" --timeout=180s
  [ "$NOTIFY_NO_DEST" = 0 ] || echo "notify has no destination: calls are refused until you create Secret $N_NS/$N_DEST_S (README "Notifications") and run: $K rollout restart deployment/notify -n $N_NS"
fi

log "waiting for agents (first start builds a golden snapshot, ~1-2 min each)"
for a in $(jq -r 'select(.kind=="Agent")|.metadata.name' <<<"$RENDERED"); do
  $K wait "agent/$a" -n $KAGENT_NS --for=condition=Ready --timeout="$AGENT_WAIT" || echo "WARN: agent $a not Ready yet: $K get agent $a -n $KAGENT_NS -o yaml"
done
$K wait remotemcpserver --all -n $KAGENT_NS --for=condition=Accepted --timeout=120s >/dev/null 2>&1 || echo "WARN: a RemoteMCPServer is not Accepted yet (vMCP may still be starting): $K get remotemcpserver -n $KAGENT_NS"

if [ -n "$CH_CM" ]; then
  log "waiting for the Kourier bindings"
  $K wait bindings.agentkourier.dev --all -n "$CH_NS" --for=condition=Ready --timeout=180s || echo "WARN: a Binding is not Ready yet: $K get bindings.agentkourier.dev -n $CH_NS"
  $K get bindings.agentkourier.dev -n "$CH_NS"
fi

log "DONE"
$K get pods -n $BRIDGE_NS
$K get agent -n $KAGENT_NS
