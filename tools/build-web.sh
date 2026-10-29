#!/usr/bin/env bash
# Compile the web client into magi/web/static/js. The output is generated and
# embedded into magi-server, so a Go build does not need TypeScript.
# The compiler is tsgo, the Go port of TypeScript (Apache-2.0); it needs no
# Node. `go run` builds the pinned version once and caches it. TSC selects
# another compiler.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tsgo=github.com/microsoft/typescript-go/cmd/tsgo@v0.0.0-20260820064610-89d5d5b2849a
rm -rf "$root/magi/web/static/js"
if [[ -n ${TSC:-} ]]; then
  "$TSC" -p "$root/magi/web/tsconfig.json"
else
  (cd "$root" && go run "$tsgo" -p "$root/magi/web/tsconfig.json")
fi
echo "web client built"
