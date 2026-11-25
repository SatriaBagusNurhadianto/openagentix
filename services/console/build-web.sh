#!/usr/bin/env bash
# Builds the Svelte app in ../../console and copies the result to web/dist, where go:embed picks it up.
# web/dist/PLACEHOLDER is the one committed file there (a clean checkout must compile); it is left alone.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
src="$here/../../console"
( cd "$src" && pnpm install --frozen-lockfile && pnpm build )
mkdir -p "$here/web/dist"
find "$here/web/dist" -mindepth 1 -not -name PLACEHOLDER -delete
cp -R "$src/build/." "$here/web/dist/"
echo "web app copied to $here/web/dist ($(find "$here/web/dist" -type f -not -name PLACEHOLDER | wc -l | tr -d ' ') files)"
