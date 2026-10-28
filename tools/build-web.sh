#!/usr/bin/env bash
# Compile the web client into magi/web/static/js. The output is generated and
# embedded into magi-server, so a Go build does not need TypeScript.
# TSC selects the TypeScript 5.9 compiler.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tsc=${TSC:-tsc}
rm -rf "$root/magi/web/static/js"
"$tsc" -p "$root/magi/web/tsconfig.json"
echo "web client built"
