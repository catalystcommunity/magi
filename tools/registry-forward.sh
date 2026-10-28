#!/usr/bin/env bash
# Keep a port-forward from 127.0.0.1:55000 to the Magi registry, and log in.
set -euo pipefail
ctx=${KUBE_CONTEXT:-default}
creds=${MAGI_REGISTRY_CREDENTIALS:-$HOME/.config/magi/registry.json}
if ! curl -s -o /dev/null http://127.0.0.1:55000/v2/; then
  # kubectl port-forward exits on some connection errors; keep restarting it.
  pidfile=/tmp/magi-registry-forward.pid
  [[ -f $pidfile ]] && kill -- "-$(cat "$pidfile")" 2>/dev/null || true
  setsid nohup bash -c "while true; do kubectl --context '$ctx' -n magi port-forward svc/registry 55000:5000; sleep 1; done" >/tmp/magi-registry-forward.log 2>&1 < /dev/null &
  echo $! > "$pidfile"
  for _ in $(seq 20); do curl -s -o /dev/null http://127.0.0.1:55000/v2/ && break; sleep 0.5; done
fi
python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["password"])' "$creds" |
  docker login 127.0.0.1:55000 -u magi --password-stdin >/dev/null
echo "registry ready at 127.0.0.1:55000 (nodes pull localhost:30500)"
