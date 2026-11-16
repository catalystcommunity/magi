#!/bin/sh
# Logical backup of every Magi database: one pg_dump (custom format) per
# database plus the roles (pg_dumpall --globals-only). Works against the
# CloudNativePG pod (CNPG's own scheduled backups are the primary backup,
# see docs/operations/backup-restore.md) and against the plain postgres pod
# of the local kind eval.
#   tools/db-backup.sh OUTDIR
# Environment: KUBECONTEXT (required), NAMESPACE (magi), DB_TARGET (the kubectl
# exec target: deploy/magi-db for the eval, pod/magi-db-1 for CloudNativePG),
# DB_CONTAINER (optional: postgres for CloudNativePG), DB_USER (magi).
# Dump files can hold personal data: OUTDIR is created with mode 700.
set -eu
out=${1:?usage: db-backup.sh OUTDIR}
ctx=${KUBECONTEXT:?set KUBECONTEXT}
ns=${NAMESPACE:-magi}
target=${DB_TARGET:-deploy/magi-db}
user=${DB_USER:-magi}
cont=${DB_CONTAINER:+-c $DB_CONTAINER}
k() { kubectl --context "$ctx" -n "$ns" exec "$target" $cont -- "$@"; }
umask 077
mkdir -p "$out"
k pg_dumpall -U "$user" --globals-only --no-role-passwords > "$out/globals.sql"
dbs=$(k psql -U "$user" -d postgres -Atc "select datname from pg_database where not datistemplate and datname <> 'postgres' order by 1")
for d in $dbs; do
  k pg_dump -U "$user" -Fc "$d" > "$out/$d.dump"
  echo "dumped $d ($(wc -c < "$out/$d.dump") bytes)"
done
echo "$dbs" > "$out/databases.txt"
( cd "$out" && shasum -a 256 *.dump globals.sql > SHA256SUMS )
