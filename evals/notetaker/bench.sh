#!/usr/bin/env bash
# Run the note-taker benchmark on local GGUF models, one at a time, with
# llama-server on this machine (README.md).
#
#   LLAMA=path/to/llama-server NOTES_EVAL=path/to/notes-eval \
#     evals/notetaker/bench.sh NAME=MODEL.gguf[:EXTRA_ARGS] ...
#
# EXTRA_ARGS are more llama-server arguments, with commas for spaces
# (for example "--n-cpu-moe,32"). Results go to evals/notetaker/results (or
# OUT). CASES is the case glob (default: the version 1 test split); NE_ARGS
# are more notes-eval arguments (for example "-teach FILE -miss FILE").
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
llama=${LLAMA:-llama-server}
eval_bin=${NOTES_EVAL:-notes-eval}
port=${PORT:-18190}
out=${OUT:-"$root/evals/notetaker/results"}
cases=${CASES:-"$root/evals/notetaker/data/test-*.jsonl"}
mkdir -p "$out"
for spec in "$@"; do
  name=${spec%%=*}
  rest=${spec#*=}
  gguf=${rest%%:*}
  extra=""
  [[ $rest == *:* ]] && extra=${rest#*:}
  echo "== $name ($gguf)"
  # shellcheck disable=SC2086
  think=()
  [[ ${REASONING:-0} == 0 ]] && think=(--chat-template-kwargs '{"enable_thinking":false}')
  "$llama" -m "$gguf" --alias "$name" --reasoning-budget "${REASONING:-0}" "${think[@]}" -ngl 99 --jinja -c "${CTX:-16384}" -np 2 \
    -fa on -ctk q8_0 -ctv q8_0 --host 127.0.0.1 --port "$port" ${extra//,/ } > "$out/$name.server.log" 2>&1 &
  pid=$!
  ok=""
  for _ in $(seq 1 120); do
    if curl -s "localhost:$port/health" | grep -q ok; then ok=1; break; fi
    if ! kill -0 "$pid" 2>/dev/null; then break; fi
    sleep 2
  done
  if [[ -z $ok ]]; then
    echo "$name: the server did not start (see $out/$name.server.log)"
    kill "$pid" 2>/dev/null || true
    continue
  fi
  # shellcheck disable=SC2086
  "$eval_bin" -cases "$cases" ${NE_ARGS:-} -url "http://127.0.0.1:$port/v1" -model "$name" \
    -workers 2 -label "$name" -out "$out/$name.jsonl" 2>/dev/null | tee "$out/$name.txt"
  kill "$pid"
  wait "$pid" 2>/dev/null || true
done
