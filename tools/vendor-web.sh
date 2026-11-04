#!/usr/bin/env bash
# Get the browser libraries of the web client from the npm registry, check
# their hashes, and copy the files that the client uses into the repository.
# No Node is necessary. The browser loads the runtime files through the
# import map in magi/web/static/index.html; tsgo reads the types.
#
#   solid-js 1.9.17  MIT  runtime: magi/web/static/vendor/solid-js/
#                         types:   magi/web/types/solid-js/
#
# The JSX types import csstype only for CSS property names; a small stub in
# magi/web/types/csstype stands in for it (the real package is 1.3 MB).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
web=$root/magi/web
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { # name version integrity
  local f=$tmp/$1-$2.tgz
  curl -fsSL -o "$f" "https://registry.npmjs.org/$1/-/$1-$2.tgz"
  local got="sha512-$(openssl dgst -sha512 -binary "$f" | base64 -w0)"
  [[ $got == "$3" ]] || { echo "$1 $2: hash $got, expected $3" >&2; exit 1; }
  mkdir -p "$tmp/$1" && tar -xzf "$f" -C "$tmp/$1"
}

fetch solid-js 1.9.17 sha512-4GAmLOWPfNV8H9Jfn2LSFTpNEG6jiOKPiwd5lnLGQtehHaX6l3TOLzJqQ7mqwB/3k1Yy3zyumFVopmc/tm34ow==

s=$tmp/solid-js/package
rm -rf "$web/static/vendor/solid-js" "$web/types/solid-js"
mkdir -p "$web/static/vendor/solid-js"
cp "$s/LICENSE" "$web/static/vendor/solid-js/LICENSE"
cp "$s/dist/solid.js" "$s/web/dist/web.js" "$s/h/dist/h.js" "$web/static/vendor/solid-js/"
for d in types web/types h/types h/jsx-runtime/types; do
  mkdir -p "$web/types/solid-js/$d"
  cp -r "$s/$d/." "$web/types/solid-js/$d/"
done
cp "$s/LICENSE" "$web/types/solid-js/LICENSE"
# The client uses the h JSX runtime (no Babel), so the core types must use
# its JSX element type too (it also allows functions as elements).
cp "$s/h/jsx-runtime/types/jsx.d.ts" "$web/types/solid-js/types/jsx.d.ts"
echo "web libraries copied"
