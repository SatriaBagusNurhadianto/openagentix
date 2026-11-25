#!/usr/bin/env bash
# Builds the Svelte app in ../../console and copies the result to web/dist, where go:embed picks it up.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
src="$here/../../console"
( cd "$src" && pnpm install --frozen-lockfile && pnpm build )
rm -rf "$here/web/dist"
mkdir -p "$here/web/dist"
cp -R "$src/build/." "$here/web/dist/"
echo "web app copied to $here/web/dist ($(find "$here/web/dist" -type f | wc -l | tr -d ' ') files)"
