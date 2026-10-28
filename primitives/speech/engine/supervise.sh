#!/usr/bin/env bash
# Run one speech engine from an env file. Restart it when it exits,
# unless --once is given (systemd restarts it then).
#   supervise.sh /path/to/stt.env [--once]
set -u
. "$1"
dir=$(cd "$(dirname "$0")" && pwd)
while :; do
  env -i PATH=/usr/bin:/bin HOME="$HOME" LANG=C.UTF-8 HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 \
    PYTHONDONTWRITEBYTECODE=1 TOKENIZERS_PARALLELISM=false OMP_NUM_THREADS="$THREADS" \
    nice -n 10 "$PYTHON" -I -u "$dir/server.py" --engine "$ENGINE" --model-dir "$MODEL_DIR" \
    --token-file "$dir/token" --port "$PORT" --threads "$THREADS"
  rc=$?
  [ "${2:-}" = --once ] && exit "$rc"
  echo "engine exited with $rc; restart in 5 s" >&2
  sleep 5
done
