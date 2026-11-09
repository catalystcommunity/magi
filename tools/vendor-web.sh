#!/usr/bin/env bash
# Get the browser libraries of the web client from the npm registry, check
# their hashes, and copy the files that the client uses into the repository.
# No Node is necessary. The browser loads the runtime files through the
# import map in magi/web/static/index.html; tsgo reads the types.
#
#   solid-js 1.9.17  MIT  runtime: magi/web/static/vendor/solid-js/
#                         types:   magi/web/types/solid-js/
#   mermaid 11.17.2  MIT  magi/web/static/vendor/mermaid/mermaid.min.js
#                         (diagrams on persona pages; loaded only there)
#   @xterm/xterm 6.0.0       MIT  magi/web/static/vendor/xterm/xterm.mjs, xterm.css
#   @xterm/addon-fit 0.11.0  MIT  magi/web/static/vendor/xterm/addon-fit.mjs
#                         (the task terminal; loaded only there)
#
# The JSX types import csstype only for CSS property names; a small stub in
# magi/web/types/csstype stands in for it (the real package is 1.3 MB).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
web=$root/magi/web
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { # name version integrity (a scoped name is @scope/name)
  local f=$tmp/${1//\//-}-$2.tgz
  curl -fsSL -o "$f" "https://registry.npmjs.org/$1/-/${1##*/}-$2.tgz"
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
fetch mermaid 11.17.2 sha512-V6K3C8EBdEsPFZXSKMJe6ppQOENxuHARr9GvHX4hh47lAbhMRD9qf4oEK7LoaRQxULMa80/qt5gHO73aCleBBg==
rm -rf "$web/static/vendor/mermaid"
mkdir -p "$web/static/vendor/mermaid"
cp "$tmp/mermaid/package/LICENSE" "$tmp/mermaid/package/dist/mermaid.min.js" "$web/static/vendor/mermaid/"
fetch @xterm/xterm 6.0.0 sha512-TQwDdQGtwwDt+2cgKDLn0IRaSxYu1tSUjgKarSDkUM0ZNiSRXFpjxEsvc/Zgc5kq5omJ+V0a8/kIM2WD3sMOYg==
fetch @xterm/addon-fit 0.11.0 sha512-jYcgT6xtVYhnhgxh3QgYDnnNMYTcf8ElbxxFzX0IZo+vabQqSPAjC3c1wJrKB5E19VwQei89QCiZZP86DCPF7g==
x=$web/static/vendor/xterm
rm -rf "$x"
mkdir -p "$x"
cp "$tmp/@xterm/xterm/package/LICENSE" "$x/LICENSE"
cp "$tmp/@xterm/xterm/package/lib/xterm.mjs" "$tmp/@xterm/xterm/package/css/xterm.css" "$x/"
cp "$tmp/@xterm/addon-fit/package/lib/addon-fit.mjs" "$x/"
cp "$tmp/@xterm/addon-fit/package/LICENSE" "$x/LICENSE.addon-fit"
# The source maps are not vendored: drop their links.
sed -i '/^\/\/# sourceMappingURL=/d' "$x/xterm.mjs" "$x/addon-fit.mjs"
echo "web libraries copied"
