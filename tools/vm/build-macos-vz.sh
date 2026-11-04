#!/usr/bin/env bash
# Build magi-vz (macOS task VMs) on a Mac runner, without root:
#   tools/vm/build-macos-vz.sh USER@HOST
# Writes ~/.local/share/magi-runner-tools/vm/magi-vz on HOST, signed ad hoc
# with the virtualization entitlement.
set -euo pipefail
host=${1:?USER@HOST}
dir=$(cd "$(dirname "$0")/macos" && pwd)
ssh "$host" 'mkdir -p ~/.local/share/magi-runner-tools/vm/src'
scp -q "$dir/magi-vz.swift" "$dir/magi-vz.entitlements" "$host:.local/share/magi-runner-tools/vm/src/"
ssh "$host" 'set -e; cd ~/.local/share/magi-runner-tools/vm
  swiftc -O -o magi-vz.new src/magi-vz.swift
  codesign -f -s - --entitlements src/magi-vz.entitlements magi-vz.new
  mv magi-vz.new magi-vz; echo "built $PWD/magi-vz"'
