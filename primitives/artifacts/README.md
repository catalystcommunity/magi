# artifacts

The artifacts primitive keeps evidence: audio, transcripts, patches, and logs.
The service stores each body one time, by its SHA-256 digest. Each owner of a
body has a separate metadata record.

## API

Contract: `contracts/artifacts.csil`. Service: `ArtifactService`.

| Operation | Function |
| --- | --- |
| `put` | Stores a body and makes or updates the record of the owner. The result has the digest `sha256:<hex>`. A second `put` of the same body by the same owner merges the labels and keeps the other fields. |
| `get` | Returns the record and the body. |
| `stat` | Returns the record only. |
| `list` | Returns the records of the caller, newest first. Filters: `label_key`, `label_key` with `label_value`, `before_ms`, `limit` (default 100, maximum 1000). |
| `label` | Adds or replaces labels on the record of the caller. |

Authorization:

- The owner is the caller. Only a caller with `artifacts:act-as` can set
  `owner` to a different subject.
- `get` and `stat` succeed if the caller owns a record for the digest, or has
  `artifacts:act-as` or `artifacts:admin`. A caller with one of these scopes
  and no record of its own gets the oldest record.
- `list` and `label` use the records of the caller only.
- If the caller cannot read a digest, the service returns 404, not 403. Thus
  a caller cannot find out if a body exists.

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `-listen` | `MAGI_LISTEN` | `:8080` |
| `-auth-keys` | `MAGI_AUTH_KEYS` | none (required). Comma-separated issuer key files. |
| `-database-url` | `MAGI_DATABASE_URL` | none (required) |
| `-artifact-dir` | `MAGI_ARTIFACT_DIR` | `/var/lib/magi/artifacts` |

Bodies are in `<dir>/sha256/<first 2 hex>/<64 hex>`. The service writes a
temporary file and then renames it. All replicas must use the same directory.

## Run

```sh
MAGI_AUTH_KEYS=/etc/magi/issuer.json \
MAGI_DATABASE_URL=postgres://magi@db.example.local/artifacts \
MAGI_ARTIFACT_DIR=/srv/artifacts \
go run ./cmd/artifacts
```

The service applies its schema at startup. `/healthz` checks the database.

## Test

```sh
MAGI_TEST_DATABASE_URL='postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
```

The integration tests make a new database and drop it at the end. They skip
when `MAGI_TEST_DATABASE_URL` is not set.

## Known limits

- The maximum body is 16 MiB. The RPC request limit is 20 MiB.
- The service does not delete records or bodies.
- `get` reads the full body into memory. There is no streaming.
- Labels: maximum 64 for each request. Keys are 1 to 256 bytes. Values are
  maximum 4096 bytes.
