# Backup and restore (PostgreSQL)

Every primitive keeps its state in one PostgreSQL cluster, one database each
(`artifacts knowledge conversation workflow execution identity corndogs composer speech`).
Losing the cluster loses conversations, knowledge, workflow history and identities.

## Production (CloudNativePG)

`deploy/base/postgres.yaml` holds two layers, rendered when the values file sets
`BACKUP_DESTINATION_PATH`:

| Layer | What | Where it goes |
| --- | --- | --- |
| Base backup + WAL archive | `ScheduledBackup magi-db-daily`, 02:00 UTC daily, plus continuous WAL archiving by `barmanObjectStore` | `BACKUP_DESTINATION_PATH` (S3-compatible bucket, optional `BACKUP_ENDPOINT_URL`) |
| Retention | `BACKUP_RETENTION`, default `30d` | the same bucket; CNPG deletes older base backups and the WAL they need |

Values file:

```sh
BACKUP_DESTINATION_PATH=s3://BUCKET/magi-db   # empty: no backup is configured
BACKUP_ENDPOINT_URL=                          # optional: non-AWS S3 endpoint
BACKUP_RETENTION=30d                          # optional
```

Credentials are never in the repository. Create the Secret by hand:

```sh
kubectl -n magi create secret generic magi-db-backup \
  --from-literal=ACCESS_KEY_ID=... --from-literal=ACCESS_SECRET_KEY=...
```

Use a bucket in another failure domain than the cluster, with versioning or
object lock if the provider has it, and a key that can write but not delete
outside the lifecycle policy. Apply with `render.py --base` as in
`deploy/README.md`. Check: `kubectl -n magi get backup,scheduledbackup,cluster magi-db`
(`Continuous Archiving` healthy, a `completed` backup).

### Restore (CloudNativePG)

Recover into a NEW cluster; never over the running one. Make a copy of the
Cluster manifest with another name and this `bootstrap` (and drop `initdb`):

```yaml
spec:
  bootstrap:
    recovery:
      source: magi-db
      # optional point in time: recoveryTarget: {targetTime: "2026-11-14 10:00:00+00"}
  externalClusters:
    - name: magi-db
      barmanObjectStore:
        destinationPath: s3://BUCKET/magi-db
        s3Credentials:
          accessKeyId: {name: magi-db-backup, key: ACCESS_KEY_ID}
          secretAccessKey: {name: magi-db-backup, key: ACCESS_SECRET_KEY}
```

When the new cluster is healthy, compare it (below), stop the services, point
`MAGI_DATABASE_URL` host (`magi-db-rw`) at the new cluster, and start them.

**Status: not tested.** CloudNativePG is not installed on the local kind
cluster, so the barman backup, the `ScheduledBackup` and the recovery cluster
were never run. The rendered manifest was checked as YAML and `kubectl apply
--dry-run=client` read it (it stopped only at "no matches for kind Cluster", the
missing CRD). Run the drill once against the real cluster before relying on it.

## Logical dump (any PostgreSQL, tested)

`tools/db-backup.sh` writes one `pg_dump -Fc` per database, the roles
(`pg_dumpall --globals-only`, no password hashes), and `SHA256SUMS` into a mode
700 directory. It runs `pg_dump` inside the database pod, so it needs only
`kubectl exec`. Keep dumps encrypted and off the cluster: they hold personal data.

```sh
export KUBECONTEXT=kind-magi-eval        # CloudNativePG: DB_TARGET=pod/magi-db-1 DB_CONTAINER=postgres DB_USER=postgres
tools/db-backup.sh ~/magi-backups/$(date +%Y%m%d-%H%M)
```

Use it before a risky change and as a second, independent copy next to the
barman backups. It has no schedule of its own.

### Restore drill

`tools/db-restore-drill.sh BACKUPDIR` verifies the checksums, starts a throwaway
`postgres:16` pod `magi-restore-drill`, restores every dump into it, and compares
row count and an md5 of all rows of every table with the live database. It
never writes to the live database and deletes the pod at the end. Run it after
each change to the backup setup and at least quarterly.

To restore for real from a dump: create a fresh Postgres (or empty databases),
`psql -f globals.sql`, `createdb -O magi NAME`, then
`pg_restore -d NAME --no-owner --exit-on-error NAME.dump` for each database.
Do it into a new instance and switch the services over; do not restore onto
live data.

### Drill result (2026-11-14, kind cluster `magi-eval`, plain `postgres:16` pod)

Dump of all 10 databases (about 80 kB) restored into a throwaway pod:

```
checksums ok
artifacts: OK (1 tables, 6 rows)       conversation: OK (12 tables, 73 rows)
execution: OK (1 tables, 0 rows)       identity: OK (6 tables, 0 rows)
knowledge: OK (8 tables, 12 rows)      speech: OK (1 tables, 0 rows)
workflow: OK (3 tables, 32 rows)       composer, corndogs, magi: no tables
```

Row counts and row checksums matched in every table. This includes the
knowledge records (3 entities, 2 claims, 5 changes) and the workflow runs
(8 runs, 18 tasks) from the task 2.5 run. The test used a quiet cluster; on a
busy one, a table that changed after the dump shows as `DIFF`, so take the dump
and run the drill with writers stopped, or accept differences in tables you can
explain.

Not covered: restore of roles with passwords, point-in-time recovery, restore
time on large data, and the CloudNativePG path above.
