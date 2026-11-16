#!/usr/bin/env bash
# Run COMMAND with a throwaway PostgreSQL 18 for the database tests.
#   tools/test-db.sh -- COMMAND...
# For example:
#   tools/test-db.sh -- sh -c 'cd primitives/identity && GOWORK=off go test -count=1 ./...'
# It needs initdb and pg_ctl on PATH (the runner tools install them in
# $TOOLS/postgres/bin). It makes a temporary data directory, starts the
# server on 127.0.0.1 at a free port, and exports
# MAGI_TEST_DATABASE_URL=postgres://magi@127.0.0.1:PORT/postgres?sslmode=disable.
# Each run is a fresh database, so tests never see rows of an earlier run.
# The script stops the server and removes the directory on every exit, and
# exits with the status of COMMAND.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
[[ ${1:-} == -- ]] || { sed -n 2,13p "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
shift
[[ $# -gt 0 ]] || { echo "tools/test-db.sh needs a command after --" >&2; exit 2; }
for tool in initdb pg_ctl; do
  command -v "$tool" >/dev/null || { echo "tools/test-db.sh needs $tool on PATH. Install the runner tools (tools/install-runner-tools.sh) or add \$TOOLS/postgres/bin to PATH." >&2; exit 1; }
done

# PostgreSQL refuses to run as root, and a task VM runs the apprentice and
# the check as root. Run initdb, pg_ctl and postgres then in a user
# namespace, where they have uid 65534. The mounts of a task VM (virtiofs)
# translate the uid of the caller only, so a plain setuid would give the
# server no write access to its data directory. COMMAND itself keeps the
# uid of the caller.
caller_uid=$(id -u)
if [[ $caller_uid == 0 ]]; then
  command -v unshare >/dev/null || { echo "tools/test-db.sh runs as root and needs unshare, to run PostgreSQL as an unprivileged user." >&2; exit 1; }
fi
as_server() {
  if [[ $caller_uid == 0 ]]; then
    unshare --map-user=65534 --map-group=65534 "$@"
  else
    "$@"
  fi
}

tmp=$(mktemp -d)
mkdir "$tmp/data"
# The server must stop and the directory must go on every exit, also when a
# test kills this script. pg_ctl stops the server only when it started. The
# signal traps exit with the status of the signal, so cleanup runs one time.
postgres=""
cleanup() {
  status=$?
  [[ -n $postgres ]] && as_server pg_ctl -D "$tmp/data" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$tmp"
  exit $status
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

as_server initdb -D "$tmp/data" -U magi --auth=trust -E UTF8 >/dev/null
# The Unix socket directory is the temp directory, so the server of this run
# never shares a socket with an other run. Ports from 55500 up: a port that
# pg_ctl refuses is in use, so try the next one.
port=55500
until as_server pg_ctl -D "$tmp/data" -o "-p $port -h 127.0.0.1 -k $tmp" -l "$tmp/postgres.log" start -w >/dev/null 2>&1; do
  port=$((port + 1))
  ((port < 55600)) || { echo "tools/test-db.sh found no free port from 55500 up" >&2; exit 1; }
done
postgres=started

export MAGI_TEST_DATABASE_URL="postgres://magi@127.0.0.1:$port/postgres?sslmode=disable"
cd "$root"
"$@"
