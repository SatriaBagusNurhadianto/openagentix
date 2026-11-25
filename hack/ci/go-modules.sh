#!/usr/bin/env bash
# Discover the Go modules CI should test. A new service with a go.mod is picked up with no edit here or in a workflow.
#   hack/ci/go-modules.sh           one module directory per line (relative to the repo root)
#   hack/ci/go-modules.sh --json    {"include":[{"dir","name","sum"}]} for a GitHub Actions matrix
#
# A module is left out (and the reason is printed on stderr) when
#   - its go.mod has a `replace` to a local path OUTSIDE this repository (it cannot build from a clean checkout), or
#   - its directory is listed in hack/ci/go-modules.skip (one directory per line, `#` comments allowed).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

SKIPFILE=hack/ci/go-modules.skip
skipped() { [ -f "$SKIPFILE" ] && sed 's/#.*//; s/[[:space:]]*$//' "$SKIPFILE" | grep -Fxq "$1"; }

# outside_replace <dir>: succeeds when go.mod has a local replace target that leaves the repository
outside_replace() {
  python3 -I - "$ROOT" "$1" <<'PY'
import os, re, sys
root, d = sys.argv[1], sys.argv[2]
txt = open(os.path.join(root, d, "go.mod")).read()
for m in re.finditer(r'=>\s*(\.{1,2}/\S*|/\S+)', txt):
    target = os.path.normpath(os.path.join(root, d, m.group(1)))
    if os.path.commonpath([root, target]) != root:
        sys.exit(0)
sys.exit(1)
PY
}

dirs=()
while IFS= read -r f; do
  d=$(dirname "$f"); d=${d#./}
  if skipped "$d"; then echo "skip $d (listed in $SKIPFILE)" >&2; continue; fi
  if outside_replace "$d"; then echo "skip $d (go.mod replaces a module with a path outside the repository)" >&2; continue; fi
  dirs+=("$d")
done < <(find . -name go.mod -not -path '*/node_modules/*' -not -path './spikes/*' -not -path './.git/*' | sort)

if [ "${1:-}" = "--json" ]; then
  for d in "${dirs[@]}"; do
    sum="$d/go.mod"; [ -f "$d/go.sum" ] && sum="$d/go.sum"
    printf '%s\t%s\t%s\n' "$d" "$(basename "$d")" "$sum"
  done | jq -R -s -c 'split("\n") | map(select(length>0) | split("\t") | {dir: .[0], name: .[1], sum: .[2]}) | {include: .}'
else
  printf '%s\n' "${dirs[@]}"
fi
