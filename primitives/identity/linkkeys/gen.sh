#!/usr/bin/env bash
# Regenerate the LinkKeys Rp wire subset from rp.csil.
set -euo pipefail
dir=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
csilgen=${CSILGEN:-csilgen}
"$csilgen" generate -q --no-schema -i "$dir/rp.csil" -t go -o "$tmp/go"
"$csilgen" generate -q --no-schema -i "$dir/rp.csil" -t go-client -o "$tmp/goc"
rm -f "$dir"/*.gen.go
cp "$tmp/go/"*.go "$tmp/goc/client.gen.go" "$dir/"
gofmt -w "$dir"
