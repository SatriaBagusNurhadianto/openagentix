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
# Switches (all optional):
#   CLUSTER=openagentix  CREATE_CLUSTER=1  KUBE_CONTEXT=kind-$CLUSTER
#   SKIP_PREREQS=1       never touch kagent/Substrate/agentgateway/ToolHive
#   FORCE_PREREQS=1      re-run the upstream installers even if their Helm releases exist (this re-applies
#                        spikes/kagent/values/substrate.yaml and DROPS any Substrate additions made by spikes/ax)
#   SKIP_SKILL=1         do not build/push the example skill; use the digest in values.yaml
#   SKIP_BRIDGE_IMAGE=1  do not build/load the bridge image
#   SKIP_KB_IMAGE=1      do not build/load the knowledge base image (services/kb)
#   SKIP_KB_INGEST=1     do not ingest components/knowledge/sample into collection "openagentix"
#   VALUES_FILE="a.yaml b.yaml"  extra values files for the chart (e.g. more agents)
#   RELEASE_NS=openagentix-system  AGENT_WAIT=900s
# Long-term memory (agents[].memory.enabled): needs pgvector in kagent's PostgreSQL; step 3b below enables it. The embedding
# ModelConfig's gateway key (Secret memory-virtual-key in the kagent namespace) is created like the agents' keys.
# The knowledge block creates (only when absent, never overwritten, so they stay stable across re-runs) the Secrets
# kb-admin, kb-postgres, kb-database and, for gateway-routed embeddings, kb-virtual-key in openagentix-knowledge.
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
docker exec "$NODE" sysctl -w net.ipv4.conf.all.proxy_arp=1 >/dev/null   # gVisor pod networking (see spikes/kagent/NOTES.md)
docker exec "$NODE" sysctl -e -w net.ipv6.conf.all.proxy_ndp=1 >/dev/null

# --- 2. prerequisites: reuse the component scripts, pinned versions live in those scripts ---------------------
helm_has(){ helm status "$1" -n "$2" >/dev/null 2>&1; }
want(){ [ "${SKIP_PREREQS:-0}" != 1 ] && { [ "${FORCE_PREREQS:-0}" = 1 ] || ! "$@"; }; }
log "prerequisites"
if want bash -c "$(declare -f helm_has); helm_has kagent $KAGENT_NS && helm_has substrate ate-system"; then
  echo "-> kagent 1.0.1 + Agent Substrate 1.0.1 (spikes/kagent/up.sh)"
  SKIP_FIRST_AGENT=1 bash spikes/kagent/up.sh
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
render(){ helm template $RELEASE $CHART -n "$RELEASE_NS" ${VALS[@]+"${VALS[@]}"} "$@" | yq -c 'select(.!=null)'; }
RENDERED=$(render) || die "chart does not render; run: helm template $RELEASE $CHART ${VALUES_FILE:+-f $VALUES_FILE}"
AGENT_KEYS=$(jq -r 'select(.kind=="ModelConfig")|[.metadata.annotations["openagentix.io/agent"],.spec.apiKeySecret,.metadata.namespace]|@tsv' <<<"$RENDERED")
NAMESPACES=$(jq -r 'select(.metadata.namespace!=null)|.metadata.namespace' <<<"$RENDERED" | sort -u)
BRIDGE_IMAGE=$(jq -r 'select(.kind=="Deployment" and .metadata.name=="telegram-bridge")|.spec.template.spec.containers[0].image' <<<"$RENDERED")
KB_DEP=$(jq -c 'select(.kind=="Deployment" and .metadata.name=="kb")' <<<"$RENDERED")   # empty when knowledge.enabled=false
# Consumers other than agents that need their own gateway virtual key (the kb, for embeddings): "user<TAB>secret<TAB>ns"
KB_KEY=$(jq -r 'select(.kind=="Deployment" and (.metadata.annotations["openagentix.io/gateway-user"]//"")!="")|[.metadata.annotations["openagentix.io/gateway-user"],.metadata.annotations["openagentix.io/gateway-key-secret"],.metadata.namespace]|@tsv' <<<"$RENDERED")
[ -z "$KB_KEY" ] || AGENT_KEYS=$(printf '%s\n%s' "$AGENT_KEYS" "$KB_KEY")

# --- 3b. long-term memory needs pgvector in kagent's PostgreSQL and config.database.vectorEnabled=true ------------------
# Only when some agent opts in (Harness spec.kagent.memory). Fresh installs get both from spikes/kagent/up.sh; this step
# brings an existing cluster up to date with the SAME scripts (pgvector.sh is idempotent, kagent-helm.sh is the one
# helm command that always carries values/kagent.yaml). It never touches kagent data. SKIP_PREREQS=1 skips it.
if [ "${SKIP_PREREQS:-0}" != 1 ] && jq -e -s 'any(.[]; .kind=="Harness" and (.spec.kagent.memory!=null))' <<<"$RENDERED" >/dev/null; then
  log "kagent long-term memory prerequisites (pgvector)"
  bash spikes/kagent/pgvector.sh
  if [ "$(helm get values kagent -n $KAGENT_NS -a -o json | jq -r '.config.database.vectorEnabled // false')" = true ]; then
    echo "kagent release: config.database.vectorEnabled already true"
  else
    echo "-> enabling config.database.vectorEnabled (spikes/kagent/kagent-helm.sh)"
    bash spikes/kagent/kagent-helm.sh
  fi
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

# Knowledge base Secrets: each is created only if absent (so re-runs never rotate a password or token), values come
# from openssl and are never printed. kb-database is built from the kb-postgres password.
if [ -n "$KB_DEP" ]; then
  KB_NS=$(jq -r '.metadata.namespace' <<<"$KB_DEP")
  kb_secret_ref(){ jq -r --arg v "$1" '.spec.template.spec.containers[0].env[]|select(.name==$v)|.valueFrom.secretKeyRef|[.name,.key]|@tsv' <<<"$KB_DEP"; }
  mk_random_secret(){  # ns name key
    $K get secret -n "$1" "$2" >/dev/null 2>&1 && { echo "secret $1/$2: kept"; return 0; }
    $K create secret generic "$2" -n "$1" --from-file="$3"=<(printf '%s' "$(openssl rand -hex 24)") >/dev/null && echo "secret $1/$2 key $3: generated"
  }
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
CM_ARGS=()
while IFS=$'\t' read -r agent secret ns; do
  [ -n "$agent" ] || continue
  $K get secret -n "$ns" "$secret" >/dev/null 2>&1 || \
    $K create secret generic "$secret" -n "$ns" --from-file=OPENAI_API_KEY=<(printf 'vk-%s' "$(openssl rand -hex 16)") >/dev/null
  h=$($K get secret -n "$ns" "$secret" -o jsonpath='{.data.OPENAI_API_KEY}' | base64 -d | shasum -a 256 | awk '{print "sha256:"$1}')
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

# --- 5. images ----------------------------------------------------------------------------------------------
SET=()
if [ "${SKIP_BRIDGE_IMAGE:-0}" != 1 ] && [[ "$BRIDGE_IMAGE" == openagentix/telegram-bridge:* ]]; then
  log "bridge image $BRIDGE_IMAGE"
  docker build -q -t "$BRIDGE_IMAGE" bridges/telegram >/dev/null
  kind load docker-image "$BRIDGE_IMAGE" --name "$CLUSTER"
fi
KB_IMAGE=$(jq -r '.spec.template.spec.containers[0].image' <<<"${KB_DEP:-null}" 2>/dev/null || true)
if [ "${SKIP_KB_IMAGE:-0}" != 1 ] && [[ "$KB_IMAGE" == openagentix/kb:* ]]; then
  log "kb image $KB_IMAGE"
  docker build -q -t "$KB_IMAGE" services/kb >/dev/null
  kind load docker-image "$KB_IMAGE" --name "$CLUSTER"
fi
if [ -n "$KB_DEP" ]; then
  # The image tag is "dev" and image ids change with every build, so key the rollout on the source content instead:
  # unchanged code = identical pod template = no restart; changed code = new hash = the pod is replaced.
  KB_HASH=$(cd services/kb && find . -type f ! -path './bin/*' ! -name '*.test' ! -name 'coverage*' -print0 | LC_ALL=C sort -z | xargs -0 shasum -a 256 | shasum -a 256 | cut -c1-16)
  SET+=(--set-string "knowledge.kb.sourceHash=$KB_HASH")
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
log "waiting for agents (first start builds a golden snapshot, ~1-2 min each)"
for a in $(jq -r 'select(.kind=="Agent")|.metadata.name' <<<"$RENDERED"); do
  $K wait "agent/$a" -n $KAGENT_NS --for=condition=Ready --timeout="$AGENT_WAIT" || echo "WARN: agent $a not Ready yet: $K get agent $a -n $KAGENT_NS -o yaml"
done
$K wait remotemcpserver --all -n $KAGENT_NS --for=condition=Accepted --timeout=120s >/dev/null 2>&1 || echo "WARN: a RemoteMCPServer is not Accepted yet (vMCP may still be starting): $K get remotemcpserver -n $KAGENT_NS"

log "DONE"
$K get pods -n $BRIDGE_NS
$K get agent -n $KAGENT_NS
