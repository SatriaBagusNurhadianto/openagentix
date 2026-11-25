#!/usr/bin/env bash
# Discover the Dockerfiles CI builds to prove they build. Only self-contained ones: a Dockerfile with a Go build stage
# (`FROM golang... AS build`) whose build context is its own directory. Images that COPY a prebuilt binary or a local content
# directory (bridges/ax-adapter, components/tools/skills) are left out.
#   hack/ci/dockerfiles.sh           one context directory per line
#   hack/ci/dockerfiles.sh --json    {"include":[{"dir","name"}]} for a matrix
set -euo pipefail
cd "$(dirname "$0")/../.."
dirs=$(find . -name Dockerfile -not -path '*/node_modules/*' -not -path './spikes/*' -not -path './.git/*' | sort \
  | while read -r f; do grep -Eiq '^FROM[[:space:]]+golang[^[:space:]]*[[:space:]]+AS[[:space:]]+build' "$f" && { d=$(dirname "$f"); echo "${d#./}"; }; done)
if [ "${1:-}" = "--json" ]; then
  printf '%s\n' "$dirs" | jq -R -s -c 'split("\n") | map(select(length>0) | {dir: ., name: (split("/")|last)}) | {include: .}'
else
  printf '%s\n' "$dirs"
fi
