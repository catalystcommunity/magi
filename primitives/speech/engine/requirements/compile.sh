#!/usr/bin/env bash
# Make the lock files (*.txt, with hashes) from the *.in files. Run it
# after a change to a *.in file. Needs uv. The locks are for Linux x86_64.
set -euo pipefail
cd "$(dirname "$0")"
plat=x86_64-manylinux_2_28
lock() { # out in python [torch index]
  local extra=()
  [ -n "${4:-}" ] && extra=(--extra-index-url "https://download.pytorch.org/whl/$4" --index-strategy unsafe-best-match)
  uv pip compile "$2" -o "$1" --python-version "$3" --python-platform "$plat" --generate-hashes \
    --no-header --quiet "${extra[@]}"
  sed -i "1i # Made by compile.sh from $2 (torch index: ${4:-pypi}). Do not edit." "$1"
}
lock stt.txt stt.in 3.13
lock runtime.txt runtime.in 3.13
lock tts-cpu.txt tts.in 3.12 cpu
lock tts-cuda.txt tts.in 3.12 cu128
lock tts-rocm.txt tts.in 3.12 rocm6.4
lock train-cpu.txt train.in 3.12 cpu
lock train-cuda.txt train.in 3.12 cu124
