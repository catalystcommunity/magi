# Corndogs 0.8.0 recovery results

Test date: 2026-09-29, America/Denver. Status: protected service tests pass.

Corndogs 0.8.0 passes the protected recovery matrix on all five configurations.
Two runs passed 146 leaf checks with no failures. The separate legacy results
add 53 compatibility passes and two isolated cluster passes.

The Corndogs service-level blocker is resolved for this tested scope. Magi can
use keyed submissions and guarded transitions. End-to-end T08/T09 approval
still needs adapter journal and external-effect tests.

The [0.7.6 report](corndogs-recovery.md) remains historical evidence.
The [adapter contract](corndogs-adapter.md) defines the next implementation.
No Magi runtime or production service was deployed.

## Result matrix

Each protected result below passed twice. A pass applies only to the stated
test, not to every possible failure.

| Check | File group | File always | PostgreSQL one server | PostgreSQL two servers | File three nodes |
| --- | --- | --- | --- | --- | --- |
| Required admission and feature checks | Pass | Pass | Pass | Pass | Pass |
| Key conflict and concurrent key replay | Pass | Pass | Pass | Pass | Pass |
| Lost submission reply through both transports and a new client | Pass | Pass | Pass | Pass | Pass |
| Lost claim and group-claim replies | Pass | Pass | Pass | Pass | Pass |
| Reject stale workers, including repeated state names | Pass | Pass | Pass | Pass | Pass |
| Mutation receipts and human-wait completion | Pass | Pass | Pass | Pass | Pass |
| Concurrent claims remain unique | Pass | Pass | Pass | Pass | Pass |
| Full process restart retains tasks, payloads, and receipts | Pass | Pass | Pass | Pass | Pass |
| Server or leader failure and rejoin | Not applicable | Not applicable | Not applicable | Pass | Pass |
| Minority cannot acknowledge; original key recovers | Not applicable | Not applicable | Not applicable | Not applicable | Pass |

## Test target

| Item | Value |
| --- | --- |
| Corndogs release | 0.8.0 |
| Release commit | `f1b6ec6086dd0341c09629791bfaa959397b3f9c` |
| Inspected checkout and chart 0.6.0 | `c2c322a72ae82c192439d9d21b8642ebb60b0ad0` |
| Server image digest | `sha256:e614bf3f88dd37d40ec7c5488bc66c06be3efbd73576eef5adfe1b9da1c64a70` |
| PostgreSQL image | 17.11, `sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd` |
| Client | Go client from the sibling checkout; no source difference from the release |
| Host and test toolchain | Linux amd64; Go 1.27.0 with the race detector |
| Protected admission | Submission keys required; task guards required |
| Receipt retention | Default one hour |

The server reports version 0.8.0 and both required features through
`GetServerInfo`. The source comparison covers the Go client, server, and
CSIL schema. These tests use the published image, not a local server build.

## Tested cases

Each protected run uses these five configurations:

- One file-backed server with group sync.
- One file-backed server with sync on each write.
- One server with PostgreSQL.
- Two servers with shared PostgreSQL.
- Three replicated file-backed servers with majority acknowledgement.

The checks cover keyed submission, request conflict, eight concurrent
submissions of one key, and queue-specific key scope. A proxy drops complete
server replies. Stream retries, cluster retries, and a new client must recover
one task identity. Claim retries must not consume another task.

Timeout and reassignment must reject old updates, completions, and
cancellations. Tests include a repeated state name and a wrong expected state.
A replay of the old claim must report that it was superseded.

Update, completion, and cancellation must recover their receipts after lost
replies. A human-wait task must complete without a claim. A submission replay
must not reopen a completed task. Eight clients must claim 64 tasks once each.

The restart test retains 24 task and submission records, four operation
receipts, and 20 payloads. It includes completed, canceled, claimed, and
human-wait tasks. It kills all servers with SIGKILL. It also kills and restarts
PostgreSQL in both PostgreSQL configurations. The final harness replays all
four guarded mutations after restart.

Multi-node tests kill a server or leader and recover both task and receipt
data. They accept new work and restart the failed node. The file-cluster test
also removes both followers. The minority must not acknowledge a write.
After quorum returns, retries with the original key must produce one task.

## Compatibility results

All 53 legacy checks passed in the first run. They use the new Go client with
unchanged legacy request shapes, not every old language client.

- Default server policies remain `compatibility`.
- Legacy submission and task processing still work.
- Legacy requests cannot claim or change a guarded task.
- The new cluster client returns uncertainty after a lost legacy submission
  reply. It does not silently replay that mutation.
- An explicit keyless retry still creates another task.
- Legacy completion and update remain unguarded. A passing compatibility
  check confirms this limitation; it does not approve those APIs for Magi.
- Legacy task and payload recovery passed after full process restart,
  including the three-node file cluster.

Both isolated legacy cluster checks also passed: leader failure with rejoin,
and minority behavior with quorum recovery.

## Rejoin readiness correction

The first protected run failed at a read through the newly restarted node.
The error was `not-leader leader=`. Earlier task and receipt recovery through
the surviving leader had passed. The restarted node's HTTP health endpoint
was ready, but leader discovery was not yet complete.

The corrected test retries only this read for a bounded period. Each call has
a one-second deadline. The outer wait is 35 seconds. A successful read with
missing or incorrect task data fails immediately. No write guard or data
comparison was relaxed. The first failed result is retained.

Both repeated file-cluster rejoin checks passed. The recovery read became
available about 0.45 and 0.61 seconds after HTTP health. These measurements
include the harness retry interval; they are not service latency guarantees.

Magi must distinguish process health from readiness to route recovery reads.
Use more than one cluster seed. An unknown leader is not evidence that a task
or receipt is absent.

## Evidence

Commands run from `experiments/corndogs-recovery`:

```sh
OPAL_RECOVERY_DOCKER=1 GOCACHE=/tmp/opal-recovery-go-cache go test -race -json -count=1 -timeout=15m .
OPAL_RECOVERY_DOCKER=1 GOCACHE=/tmp/opal-recovery-go-cache go test -race -json -count=2 -timeout=15m -run '^TestProtectedRecovery$' .
GOCACHE=/tmp/opal-recovery-go-cache go vet ./...
```

- [First release run](evidence/experiments.md#experiment-005): 127 leaf passes, one leaf failure. Package exit status is failure.
- [Repeated protected runs](evidence/experiments.md#experiment-006): 146 leaf passes, no failures or skips. Package exit status is success.

The result counter excludes parent tests. Use `summarize.mjs` to reproduce
the totals. A compile-only run without Docker opt-in skips the integration
tests and is not recovery evidence.

Final harness hashes:

```text
recovery_test.go  05d5701f3a03cf988e1f803167488b73c7f078695dd55c3330034d2d9645d34c
protected_test.go 74678cb61ab707231864bfe9f20bce17370731eb7f0e1dafc608436252ec3918
```

The first run used the earlier protected harness, before the bounded rejoin
wait and added terminal and post-restart replay assertions.

`go vet` passed. Neither recorded run reported a race-detector warning.
All test containers, anonymous volumes, and networks were removed. A final
label-based check found no remaining test containers or networks. Removed
synthetic data cannot be recovered; raw test records remain. No Corndogs
source or persistent deployment was changed. No Git staging, commit, or push
was performed.

## Limits and deployment rules

These tests cover process crashes on one host with retained storage. They do
not prove recovery from power loss, disk loss, arbitrary network partitions,
backup restore, or independent failure domains. PostgreSQL replication was
not tested. File interval sync, disabled sync, and asynchronous replication
were not tested. The race detector covers the harness and client, not the
prebuilt server.

Receipt expiry was not exercised. Corndogs documents a one-hour default and
permits a new task from the same key after expiry. Magi must retain its own
intent records and stop blind retries after its safe deadline. Configure
retention for the intended recovery delay before unattended deployment.

Set both admission policies to `required` for a dedicated Magi service.
The release deliberately retains compatibility defaults. Finish all server
upgrades before admitting protected tasks. Mixed-version file clusters and
rollback to 0.7.x after protected work are not supported.

The Corndogs boundary of T08 and T09 is the scope of this experiment.
No worker business effect, Magi journal crash, Longhouse event, or durable
publication was executed. Do not claim end-to-end exactly-once business work.

## Next Actions

1. Use the adapter contract for W05. Add real journal and external-effect
   recovery tests before accepting unattended workflows.
2. Continue W02 identity and receiving-service policy tests. Then compile
   W03 contracts with CSIL. The Corndogs protocol requires no new task engine.
