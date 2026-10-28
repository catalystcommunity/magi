#!/usr/bin/env bash
# End-to-end check of a running installation through the public API.
#   MAGI_SERVER=https://magi.example.local MAGI_TOKEN_FILE=token tools/demo-check.sh [--code-change] [--approve]
# It uses only the command-line client, so it tests what any client can do.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
magi=${MAGI_CLI:-$root/.build/bin/magi}
[[ -x $magi ]] || (cd "$root/magi" && go build -o "$magi" ./cmd/magi)
clip="$root/primitives/speech/testdata/calendar.opus"
step() { echo; echo "=== $*"; }

step "text conversation"
timeout 300 "$magi" say "Hello Magi. Reply with one short sentence."

step "knowledge: remember, then recall in a new chat"
timeout 300 "$magi" say "My dentist is Dr. Rivera at Maple Street Dental. Their booking system is Zocdoc."
timeout 300 "$magi" say "Who is my dentist and how do I book?"

step "voice in, spoken reply out"
timeout 300 "$magi" say -speak -audio "$clip"

if [[ ${1:-} == --code-change ]]; then
  step "code change through the conversation"
  out=$(timeout 600 "$magi" say "Please make a change to the Magi repository: add a section named 'Try it' to the end of README.md that tells people to read deploy/README.md to install Magi and magi/README.md to use the command-line client. Keep it to three sentences in Simplified Technical English.")
  echo "$out"
  run=$(grep -o 'workflow-run run_[A-Za-z0-9_-]*' <<<"$out" | head -1 | awk '{print $2}')
  [[ -n $run ]] || { echo "no task started"; exit 1; }
  step "waiting for task $run to reach review"
  for _ in $(seq 240); do
    status=$("$magi" task "$run" | sed -n 2p)
    grep -qE 'status (waiting|failed|gave-up|rejected)' <<<"$status" && break
    sleep 10
  done
  "$magi" task "$run"
  "$magi" diff "$run" | head -60
  if [[ ${2:-} == --approve ]]; then
    step "approving $run"
    "$magi" decide "$run" approve -m "Approved by demo-check."
    for _ in $(seq 120); do
      status=$("$magi" task "$run" | sed -n 2p)
      grep -qE 'status (released|merged|release-rolled-back|release-failed|conflict|failed)' <<<"$status" && break
      sleep 10
    done
    "$magi" task "$run"
  fi
fi
echo; echo "demo-check finished"
