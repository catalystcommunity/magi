# Corndogs 0.7.6 recovery results

Test date: 2026-09-28. Status: historical results for Corndogs 0.7.6.

Corndogs 0.8.0 now implements the requested resilience contract. See the
[new release tests](corndogs-recovery-080.md) for current evidence and limits.
The findings and unmet requirements below describe 0.7.6, not the new release.
The original implementation request is no longer in the Corndogs checkout.
Its replacement is the [released contract](../../corndogs/docs/resilience.md).

## Conclusion

Single-node file storage and PostgreSQL retained the tested acknowledged
state after process crashes. Two servers sharing PostgreSQL continued after
one server failed. A fresh three-node file cluster also passed isolated
leader-failure and rejoin tests.

This does not establish safe unattended workflow recovery for Magi yet.
The tests reproduced these gaps:

1. A lost submission reply followed by a retry creates another task.
2. The Go cluster-aware client can make that retry without the application
   issuing a second call.
3. An old worker can complete a task after another worker claims it.
4. An update with the wrong `current_state` still changes the task.
5. The replicated file configuration fails the full-cluster restart test.

T08 and T09 remain unmet. These results do not require another task engine.
They identify specific Corndogs contracts that Magi must resolve.

## Test target

| Item | Recorded value |
| --- | --- |
| Server release | Corndogs 0.7.6 |
| Release commit | `21d018211ec895c3af5785bc18c446db6c77ee53` |
| Inspected checkout | `7ec909453f9da65de815a0a79d3bcdb5eb4cc6bf` |
| Source comparison | Server, Go client, and CSIL source match the release commit; only the chart version differs |
| Server image digest | `sha256:6f4776c312c19016e1d7b45c500c9a8d238cb8699611ce82a16b149b858f576b` |
| PostgreSQL image | PostgreSQL 17.11, `sha256:0b657ff48d7f76a1e907f381b1693eb4f2bf54c1d2df4feb6743d7dc601768dd` |
| Test host | Linux amd64, local Docker 29.7.2 |
| Test toolchain | Go 1.27.0; race detector used in repeated runs |

The [harness](../experiments/corndogs-recovery/recovery_test.go) uses the real
Go client and CSIL-RPC transport. The database is not a mock. All test data
and credentials are synthetic. No production service or cluster was changed.
The pre-existing untracked Corndogs file was left unchanged.

## Result matrix

Pass means the stated check passed in this test. It is not a general durability
or availability guarantee.

| Check | File group | File always | PostgreSQL, one server | PostgreSQL, two servers | File cluster, three nodes |
| --- | --- | --- | --- | --- | --- |
| Retain state and payload after all processes restart | Pass | Pass | Pass | Pass | Fail: EOF during a retained-task claim |
| 64 tasks claimed once by eight clients | Pass | Pass | Pass | Pass | Pass |
| Timeout requires an explicit sweep | Confirmed | Confirmed | Confirmed | Confirmed | Confirmed |
| Retry a lost submit reply without duplication | Fail | Fail | Fail | Fail | Fail |
| Cluster-aware client retries without duplication | Fail | Fail | Fail | Fail | Fail |
| Reconcile lost completion reply by known task ID | Pass | Pass | Pass | Pass | Pass |
| Reject stale completion with a different state | Fail | Fail | Fail | Fail | Fail |
| Reject stale completion after the state name repeats | Fail | Fail | Fail | Fail | Fail |
| Reject update with wrong expected state | Fail | Fail | Fail | Fail | Fail |
| One server or leader fails; new work and rejoin succeed | Not applicable | Not applicable | Not applicable | Pass | Pass in fresh isolated fixtures; fails after the full restart sequence |
| Minority does not acknowledge a write during the deadline | Not applicable | Not applicable | Not applicable | Not applicable | Pass in fresh isolated fixtures |

The restart case uses 24 tasks. Before SIGKILL it completes one, cancels one,
claims one, and changes one to a human-wait state. It compares metadata before
and after restart. It also claims 20 retained payloads. PostgreSQL itself is
killed and restarted in both PostgreSQL configurations.

Completed and canceled records do not retain the live priority field. An
early harness check incorrectly expected that field to remain 7. The corrected
test compares archived metadata with the pre-crash value. This was a test
assumption error, not evidence of crash data loss.

## Lost submission reply

The proxy forwards the request and reads the complete server response. It
then closes the caller connection without returning that response.

The task count is one before retry. An explicit retry creates a second UUID.
Both tasks contain the same payload. A UUID assigned only by the server cannot
help a caller that never received it. The public API has no caller submission
key or non-mutating lookup by such a key.

The same fault with `ClusterTransport` is more difficult for a consumer to
detect. One application call returns success, but two tasks exist. The
transport retries connection failures, not only explicit leader redirects.
[Transport source](../../corndogs/clients/corndogs/cluster_transport.go).

This proves duplicate task creation. The test does not execute a business
operation twice. Whether duplicate tasks cause duplicate effects depends on
the worker and the destination service.

## Stale workers and transitions

The test uses this sequence:

1. Worker A claims a task.
2. The timeout sweep returns it to the ready state.
3. Worker B claims the same UUID.
4. Worker A submits its old completion request.
5. Corndogs completes the task that worker B now holds.

This succeeds both when B uses a different state name and when B uses A's
old state name. Thus, checking only a state string would not solve the second
case. The API needs a distinct claim generation or equivalent revision check.

An update with a wrong expected state also succeeds. Source inspection agrees
with these results: completion and update locate the task by UUID without
an atomic comparison of the supplied `current_state`.
[File store](../../corndogs/corndogs/server/store/filestore/boltstore.go),
[PostgreSQL store](../../corndogs/corndogs/server/store/postgresstore/postgresstore.go).

A caller-side read followed by an update is not an atomic guard. Another
worker or timeout sweep can change the task between those calls.

## Completion and timeout recovery

When the caller knows the task UUID, a lost completion reply can be reconciled
through `GetTaskStateByID`. It returns the completed archive record. Repeating
`CompleteTask` returns no task in these tests. Consumers must query the known
ID instead of treating that empty repeat response as proof of failure.

This recovers terminal metadata, not an arbitrary business result or an
external effect receipt. The tested task contract does not store those results.

An expired claim remains in its working state until `CleanUpTimedOut` runs.
The test waits beyond the deadline to verify this, then runs the sweep and
reclaims the same UUID. The sweep must have a defined owner and health check.
Starting another server does not replace the sweep.

## Replicated file storage

Fresh isolated fixtures passed leader-failure and rejoin three times.
Each fixture retained 24 acknowledged tasks, accepted a new task after the
leader was killed, and made that new task visible on the restarted node.
Measured time from kill through the successful new write was approximately
5.06 to 5.76 seconds, including the harness's leader-stability wait.
This is not a service latency limit.

Fresh isolated minority tests also passed three times. With both followers
stopped, the leader returned no successful write response within the
five-second client deadline. After the followers restarted, another write
succeeded. The timed-out request has an uncertain outcome; this test does not
prove that no local mutation occurred.

The full restart sequence repeatedly fails with `connection closed: EOF`
when claiming retained work. A later leader-loss test in that same fixture
can read the 24 new task states but times out when it tries a new submission:
`clustering: write not committed to quorum in time`.

The first harness version also tried to kill an already stopped node after
a failed subtest. That cleanup error is not a Corndogs result. The revised
harness restores stopped nodes, uses separate isolated fixtures, and waits
for more than one leader metric sample. The full-restart failure remains.

The exact server cause is not established. Investigate replication-position
recovery and the post-restart claim path with a focused server test. Do not
claim that the observed EOF alone proves disk data loss. Do not approve this
mode for Magi recovery until the full restart case passes.

## Proposed Corndogs contract changes

These are requirements for review, not implemented changes.

| Operation | Required behavior |
| --- | --- |
| Submit | Accept a caller operation key. Atomically retain the key, request digest, and task UUID with task creation. Same key and same input return the original identity. Different input returns a conflict. |
| Submission lookup | Resolve the caller key without claiming or changing a task. Retain the mapping across completion for the declared retry period. |
| Claim | Return a generation that changes on each reassignment. A repeated state name must not reuse an old generation. |
| Update, complete, cancel | Atomically check the queue, expected revision or claim generation, and requested transition. Reject stale writes before mutation. |
| Mutation recovery | Retain a command receipt, or provide enough revision and operation identity to reconcile a lost reply. |
| Client retry | Retry only when non-execution is known, or when the request has a server-enforced idempotency key. Report uncertain outcomes otherwise. |

All storage modes must enforce the same contract. Preserve compatibility
through explicit versioning or new operations; do not silently reinterpret
old callers. Use T08/T09 as conformance tests for the new behavior.

An Magi adapter can reject old worker results and suppress duplicate logical
work with its own durable records. It cannot make these Corndogs writes atomic
through a read-then-write check. Requiring all effects to pass through that
adapter also does not protect callers that write to Corndogs directly.
Prefer a narrow service contract change over another editable task-state store.

## Evidence and scope

The [experiment instructions](../experiments/corndogs-recovery/README.md)
describe commands, fault injection, configuration, and cleanup. Raw results:

- [Initial matrix](evidence/experiments.md#experiment-003).
- [Early repeated race runs](evidence/experiments.md#experiment-004).
- [Independent cluster cases](evidence/experiments.md#experiment-002).
- [Revised matrix](evidence/experiments.md#experiment-001).

The early runs retain evidence of harness problems. Use the revised matrix
for the main comparison and the independent cluster cases for isolated
failover and minority behavior. The source hash for the revised harness is
`b60336818a969481fcd532713572815f4341be057718818434c1c84b52766da2`.

The revised matrix ran twice with the race detector: 40 leaf checks passed
and 56 failed. Those failures include the unmet T08/T09 checks and the
replicated-file restart sequence. The separate cluster tests added six
passing checks across three runs. No race-detector warning was reported.
The main suite correctly exits with failure; the isolated cluster suite
exits successfully. `go vet` also passed.

All test containers, their anonymous volumes, and their test networks were
removed. A final label-based check found no remaining test containers or
networks. The removed synthetic data is not retained; the raw result files
remain available. No Corndogs source, commit, or production deployment changed.

All nodes shared one host. This work does not test host power loss, failed
disks, independent failure domains, network partitions, PostgreSQL replication,
backup restore, or complete audit retention. File `interval`, file `never`,
and asynchronous replication were not tested. The race detector covers the
harness and Go client, not the server binary.

## Next Actions

Use the [0.8.0 report](corndogs-recovery-080.md) and
[Magi adapter contract](corndogs-adapter.md) for current work. Keep these old
results as regression evidence; do not repeat the completed implementation
request from this historical report.
