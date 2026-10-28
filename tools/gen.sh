#!/usr/bin/env bash
# Regenerate all code from contracts/*.csil.
# Go server and client code goes to <module>/api. Other languages go to clients/.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
csilgen=${CSILGEN:-csilgen}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# contract name -> Go module directory
declare -A godir=(
  [artifacts]=primitives/artifacts
  [knowledge]=primitives/knowledge
  [conversation]=primitives/conversation
  [workflow]=primitives/workflow
  [execution]=primitives/execution
  [source]=primitives/source
  [speech]=primitives/speech
  [inference]=primitives/inference
  [release]=primitives/release/goapi
  [identity]=primitives/identity
  [magi]=magi
)

for csil in "$root"/contracts/*.csil; do
  name=$(basename "$csil" .csil)
  if [[ -n "${godir[$name]:-}" ]]; then
    out="$root/${godir[$name]}"; [[ $name != release ]] && out="$out/api"
    rm -rf "$tmp/go" "$tmp/goc"
    "$csilgen" generate -q --no-schema -i "$csil" -t go -o "$tmp/go"
    "$csilgen" generate -q --no-schema -i "$csil" -t go-client -o "$tmp/goc"
    mkdir -p "$out"
    rm -f "$out"/*.gen.go
    cp "$tmp/go/"*.go "$tmp/goc/client.gen.go" "$out/"
    (cd "$root/tools/dispatchgen" && go run . "$out")
    gofmt -w "$out"
  fi
  py="$root/clients/python/magi_$name"
  rm -rf "$py" && "$csilgen" generate -q --no-schema -i "$csil" -t python-client -o "$py"
  ts="$root/clients/typescript/$name"
  rm -rf "$ts" && "$csilgen" generate -q --no-schema -i "$csil" -t typescript-client -o "$ts"
done

# The release controller is a Rust server.
if [[ -f "$root/contracts/release.csil" ]]; then
  rs="$root/primitives/release/src/api"
  rm -rf "$rs" && "$csilgen" generate -q --no-schema -i "$root/contracts/release.csil" -t rust -o "$rs"
fi
echo "generated"
