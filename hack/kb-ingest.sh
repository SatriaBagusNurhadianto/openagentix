#!/usr/bin/env bash
# Ingest a directory of markdown files into a knowledge base collection through the kb admin API.
#
#   hack/kb-ingest.sh <dir> [collection]          default collection: openagentix
#
# - Idempotent: every file becomes one PUT /v1/collections/<collection>/documents/<id>, which replaces that
#   document's chunks. Re-running leaves exactly one copy of each document.
# - Document id = path relative to <dir>, without the .md extension, "/" replaced by ":" and any character the API
#   does not allow replaced by "-"  (adr/0001-x.md -> adr:0001-x). Two files that map to the same id are an error.
# - Title = the first "# " heading of the file, else the file name.
# - The admin token is read from the cluster Secret into a shell variable and handed to curl through a config file
#   descriptor, so it is never printed and never appears in argv.
# - Transport: `kubectl port-forward` to the kb Service (arrives on the pod's loopback, so no NetworkPolicy entry is
#   needed). Set KB_URL (and KB_ADMIN_TOKEN) to skip the port-forward, e.g. from an in-cluster Job.
#
# Env: KB_NAMESPACE=openagentix-knowledge  KB_SERVICE=kb  KB_ADMIN_SECRET=kb-admin  KB_ADMIN_KEY=KB_ADMIN_TOKEN
#      KB_URL (e.g. http://kb.openagentix-knowledge.svc.cluster.local:8080)  KB_ADMIN_TOKEN (with KB_URL)
#      KUBECTL=kubectl  KUBE_CONTEXT (optional)  KB_INGEST_DRY_RUN=1 (list ids and titles, contact nothing)
set -euo pipefail

DIR=${1:-}; COLLECTION=${2:-openagentix}
[ -n "$DIR" ] && [ -d "$DIR" ] || { echo "usage: $0 <dir-with-markdown> [collection]" >&2; exit 2; }
[[ "$COLLECTION" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || { echo "ERROR: invalid collection name '$COLLECTION'" >&2; exit 2; }

NS=${KB_NAMESPACE:-openagentix-knowledge}; SVC=${KB_SERVICE:-kb}
SECRET=${KB_ADMIN_SECRET:-kb-admin}; SKEY=${KB_ADMIN_KEY:-KB_ADMIN_TOKEN}
K=${KUBECTL:-kubectl}; KARGS=(); [ -z "${KUBE_CONTEXT:-}" ] || KARGS+=(--context "$KUBE_CONTEXT")
DRY=${KB_INGEST_DRY_RUN:-0}
for c in jq curl; do command -v "$c" >/dev/null || { echo "ERROR: $c is required" >&2; exit 1; }; done

files=()
while IFS= read -r f; do files+=("$f"); done < <(find "$DIR" -type f \( -name '*.md' -o -name '*.markdown' \) | LC_ALL=C sort)
[ ${#files[@]} -gt 0 ] || { echo "ERROR: no markdown files under $DIR" >&2; exit 1; }

doc_id(){  # path relative to DIR -> API-safe id
  local rel=${1#"${DIR%/}"/}
  rel=${rel%.md}; rel=${rel%.markdown}
  rel=${rel//\//:}
  rel=$(printf '%s' "$rel" | LC_ALL=C sed -E 's/[^A-Za-z0-9._:-]/-/g; s/^[^A-Za-z0-9]+//' | cut -c1-200)
  printf '%s' "$rel"
}
doc_title(){  # first "# " heading, else the file name without extension
  local t
  t=$(awk '/^# /{sub(/^# +/,""); print; exit}' "$1")
  [ -n "$t" ] || t=$(basename "$1"); t=${t%.md}; t=${t%.markdown}
  printf '%s' "$t" | cut -c1-500
}

ids=(); seen=""
for f in "${files[@]}"; do
  id=$(doc_id "$f"); [ -n "$id" ] || { echo "ERROR: cannot derive an id from $f" >&2; exit 1; }
  case $'\n'"$seen"$'\n' in *$'\n'"$id"$'\n'*) echo "ERROR: two files map to document id '$id'" >&2; exit 1;; esac
  seen+="$id"$'\n'; ids+=("$id")
done

if [ "$DRY" = 1 ]; then
  for i in "${!files[@]}"; do printf '%s/%s\t%s\t%s\n' "$COLLECTION" "${ids[$i]}" "$(doc_title "${files[$i]}")" "${files[$i]}"; done
  exit 0
fi

PF_PID=""; PF_LOG=""
cleanup(){
  if [ -n "$PF_PID" ]; then kill "$PF_PID" 2>/dev/null || true; wait "$PF_PID" 2>/dev/null || true; fi
  [ -z "$PF_LOG" ] || rm -f "$PF_LOG"
}
trap cleanup EXIT

TOKEN=${KB_ADMIN_TOKEN:-}
if [ -z "${KB_URL:-}" ]; then
  [ -n "$TOKEN" ] || TOKEN=$($K ${KARGS[@]+"${KARGS[@]}"} get secret "$SECRET" -n "$NS" -o jsonpath="{.data.$SKEY}" | base64 -d)
  PF_LOG=$(mktemp)
  $K ${KARGS[@]+"${KARGS[@]}"} port-forward -n "$NS" "svc/$SVC" :8080 >"$PF_LOG" 2>&1 &
  PF_PID=$!
  PORT=""
  for _ in $(seq 1 30); do
    PORT=$(sed -nE 's/^Forwarding from 127\.0\.0\.1:([0-9]+) .*/\1/p' "$PF_LOG" | head -1)
    [ -n "$PORT" ] && break
    kill -0 "$PF_PID" 2>/dev/null || { echo "ERROR: port-forward failed: $(cat "$PF_LOG")" >&2; exit 1; }
    sleep 1
  done
  [ -n "$PORT" ] || { echo "ERROR: port-forward did not come up" >&2; exit 1; }
  KB_URL="http://127.0.0.1:$PORT"
fi
[ -n "$TOKEN" ] || { echo "ERROR: no admin token (set KB_ADMIN_TOKEN when you use KB_URL)" >&2; exit 1; }
KB_URL=${KB_URL%/}

for _ in $(seq 1 30); do curl -fsS -m 3 "$KB_URL/readyz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS -m 3 "$KB_URL/readyz" >/dev/null || { echo "ERROR: kb at $KB_URL is not ready" >&2; exit 1; }

ok=0; failed=0
for i in "${!files[@]}"; do
  f=${files[$i]}; id=${ids[$i]}; title=$(doc_title "$f")
  if ! grep -q '[^[:space:]]' "$f"; then echo "skip   $COLLECTION/$id (blank)"; continue; fi
  enc=$(jq -rn --arg v "$id" '$v|@uri')
  # -K reads the header from a descriptor: the token is not in argv. The body is built by jq (proper escaping).
  code=$(jq -n --rawfile text "$f" --arg title "$title" '{title:$title,text:$text}' \
    | curl -sS -m 150 -o /dev/null -w '%{http_code}' -X PUT -K <(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN") \
        -H 'Content-Type: application/json' --data-binary @- "$KB_URL/v1/collections/$COLLECTION/documents/$enc") || code=000
  if [ "$code" = 200 ]; then ok=$((ok+1)); printf 'put    %s/%s  "%s"\n' "$COLLECTION" "$id" "$title"
  else failed=$((failed+1)); printf 'FAILED %s/%s  http %s\n' "$COLLECTION" "$id" "$code" >&2; fi
done
echo "ingested $ok document(s) into collection '$COLLECTION', $failed failed"
[ "$failed" -eq 0 ]
