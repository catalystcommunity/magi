#!/bin/bash
# Restore drill: restore the dumps of tools/db-backup.sh into a throwaway
# Postgres pod (never into the live database) and compare the row count and a
# checksum of the rows of every table with the live database. Deletes the pod at the end.
#   tools/db-restore-drill.sh BACKUPDIR
# Environment: KUBECONTEXT (required), NAMESPACE (magi), DB_TARGET (deploy/magi-db),
# DB_CONTAINER, DB_USER (magi), PG_IMAGE (postgres:16).
# The live row counts move while services write, so a mismatch in a table
# that changed since the dump is reported, not hidden: read the table list.
set -eu
dir=${1:?usage: db-restore-drill.sh BACKUPDIR}
ctx=${KUBECONTEXT:?set KUBECONTEXT}
ns=${NAMESPACE:-magi}
target=${DB_TARGET:-deploy/magi-db}
user=${DB_USER:-magi}
cont=${DB_CONTAINER:+-c $DB_CONTAINER}
img=${PG_IMAGE:-postgres:16}
pod=magi-restore-drill
kc() { kubectl --context "$ctx" -n "$ns" "$@"; }
live() { kc exec -i "$target" $cont -- psql -U "$user" -Atq "$@"; }
trap 'kc delete pod "$pod" --ignore-not-found --wait=false >/dev/null' EXIT
( cd "$dir" && shasum -a 256 -c SHA256SUMS >/dev/null ) && echo "checksums ok"
kc delete pod "$pod" --ignore-not-found >/dev/null
kc run "$pod" --image="$img" --restart=Never --env=POSTGRES_PASSWORD=drill --env=POSTGRES_USER=magi >/dev/null
kc wait --for=condition=Ready "pod/$pod" --timeout=120s >/dev/null
i=0; until kc exec "$pod" -- pg_isready -U magi -q 2>/dev/null; do i=$((i+1)); [ $i -lt 30 ] || { echo "drill pg not ready"; exit 1; }; sleep 2; done
t() { kc exec -i "$pod" -- "$@"; }
t psql -U magi -d postgres -q -v ON_ERROR_STOP=0 < "$dir/globals.sql" >/dev/null 2>&1 || true
status=0
for d in $(cat "$dir/databases.txt"); do
  t psql -U magi -d postgres -qc "create database \"$d\" owner magi" >/dev/null 2>&1 || true
  t pg_restore -U magi -d "$d" --no-owner --exit-on-error < "$dir/$d.dump"
  # one "table|count|md5 of all rows" line per table
  q="select format('select %L||''|''||count(*)||''|''||coalesce(md5(string_agg(x::text, '''' order by x::text)),''-'') from %I.%I x', schemaname||'.'||tablename, schemaname, tablename) from pg_tables where schemaname not in ('pg_catalog','information_schema') order by 1"
  sql=$(t psql -U magi -d "$d" -Atc "$q" | sed 's/$/;/')
  [ -n "$sql" ] || { echo "$d: no tables"; continue; }
  r=$(printf '%s\n' "$sql" | t psql -U magi -d "$d" -Atq)
  l=$(printf '%s\n' "$sql" | live -d "$d")
  if [ "$r" = "$l" ]; then
    echo "$d: OK ($(printf '%s\n' "$r" | wc -l | tr -d ' ') tables, $(printf '%s\n' "$r" | awk -F'|' '{s+=$2} END{print s}') rows)"
  else
    echo "$d: DIFF"; diff -u <(printf '%s\n' "$l") <(printf '%s\n' "$r") || true; status=1
  fi
done
exit $status
