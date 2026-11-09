# workflow

The workflow primitive keeps workflow definitions and runs. A task step goes
to a worker through a Corndogs queue. A human step waits for a decision.

## API

Contract: `contracts/workflow.csil`, service `WorkflowService`.

| Operation | Who can call | What it does |
| --- | --- | --- |
| `put-definition` | Any caller. The first writer of a name owns it. Later writers must be the owner or have `workflow:act-as` or `workflow:admin`. | Validates the definition and stores the next revision. |
| `get-definition` | Any caller | Returns the latest revision. |
| `start` | Any caller. Set `owner` only with `workflow:act-as` or `workflow:admin`. | Starts a run on the latest revision. The run keeps that revision. The same `owner` and `key` return the same run. |
| `get-run`, `watch-run` | The owner, a decider of the waiting step, or `workflow:act-as`, `workflow:admin`, `workflow:decide`, `workflow:worker` | `watch-run` waits up to 30 seconds for `updated_ms` to become greater than `after_ms`. |
| `list-runs` | Any caller | Returns the caller's runs. With `workflow:act-as` or `workflow:admin`, returns all runs. With `waiting_for_me`, returns the runs that wait for a decision of the caller. |
| `claim`, `heartbeat`, `progress`, `complete` | `workflow:worker` or `workflow:admin` | Claim takes a task from a queue and waits up to `wait_ms` (maximum 30 seconds). The lease is `lease_ms` (default 60 seconds). |
| `decide` | The owner, a decider of the step, or `workflow:decide`, `workflow:act-as`, `workflow:admin` | Decides a waiting human step. The comment goes into `vars` as `<step>.comment`. |
| `cancel` | The owner, or `workflow:act-as`, `workflow:admin` | Cancels the run and closes its queue tasks. |

Step rules:

- An outcome that `next` does not map, or that maps to `""`, finishes the run
  as `succeeded`.
- Outcome `fail` without a mapping fails the run.
- Outcome `retry` without a mapping offers the task step again. After
  `max_attempts` (default 3) the run fails.
- When a lease expires, the step is offered again with the next attempt. After
  `max_attempts` the run fails.
- A claim token is good for one attempt only. A stale token gets error 409.
- Task outputs merge into `vars`.
- A human step can name `deciders`: subjects, or run variables as
  `${name}`. The run lists them in `waiting_for` while it waits. A decider
  can read the run and decide the step; after the step, the decider cannot
  read the run. An investigation uses this to ask another person.
- A human step with `wait_ms` ends after that time with `timeout_outcome`
  (default `timeout`). `decided_by` is `workflow:timeout`. The sweep does
  this.
- `wait_until` is a time (Unix milliseconds), or a run variable as
  `${name}` that holds one. The wait of the step ends then. An empty
  variable gives no time. With `wait_ms` too, the earlier time ends the
  wait. A time in the past ends the wait on the next sweep.
- `MemQueue` (`server/memqueue.go`) is a queue in memory for the tests of
  other modules. It is not for a service.

## Design

- PostgreSQL holds definitions, runs, task attempts, and leases. A transition
  locks the run row, so many replicas can run at the same time.
- Corndogs holds the queue tasks and their claims. The service uses only
  keyed submissions and guarded operations (Corndogs 0.8.0 or later). The
  submission key is `<run>/<history index>`. The Corndogs tasks have no
  timeout. This service controls the lease.
- Each replica runs a sweep every 500 ms. The sweep sends queue work again
  after a failure, expires leases, and returns a queue task to pending when
  a claim got to Corndogs but not to the database.

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `-listen` | `MAGI_LISTEN` | `:8080` |
| `-auth-keys` | `MAGI_AUTH_KEYS` | required; comma-separated issuer key files |
| `-database-url` | `MAGI_DATABASE_URL` | required |
| `-corndogs-url` | `MAGI_CORNDOGS_URL` | required; comma-separated `host:port` (a `tcp://` prefix is permitted) |
| `-corndogs-queue-prefix` | `MAGI_CORNDOGS_QUEUE_PREFIX` | `magi.workflow.` |

Use one Corndogs installation, or one queue prefix, for each workflow
database.

## Run

```sh
go run ./cmd/workflow
```

Corndogs for development (file storage in memory):

```sh
docker run -d --name magi-test-corndogs --cpus 1 --memory 512m \
  -p 127.0.0.1:55080:5080 --tmpfs /data:uid=65532,gid=65532,size=64m \
  -e STORAGE_BACKEND=file -e CORNDOGS_FILESTORE_DIR=/data \
  -e CORNDOGS_SUBMISSION_KEY_POLICY=required -e CORNDOGS_TASK_GUARD_POLICY=required \
  containers.catalystsquad.com/public/catalystcommunity/corndogs:0.8.0
```

## Test

```sh
MAGI_TEST_DATABASE_URL='postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable' \
MAGI_TEST_CORNDOGS_URL=127.0.0.1:55080 go test -race ./...
```

The tests run each case with an in-memory queue and with Corndogs. A test
skips when its variable is not set.

## Known limits

- `watch-run` reads the database every 500 ms. It does not use LISTEN/NOTIFY.
- `claim` polls Corndogs every 200 ms.
- A lease expires on the next sweep, up to 500 ms after the lease ends.
- Corndogs keeps submission receipts for `CORNDOGS_RECEIPT_RETENTION`
  (default 1 hour). If Corndogs is unavailable for longer than that, a
  repeated submit can make a second queue task. The service closes a queue
  task that does not match the current attempt.
- There is no limit on definition size or the number of runs.
