# Corndogs workflow journal

The `workflow` package stores exact Corndogs requests before it sends them.
Corndogs owns task state. The journal owns operation intent, recovery evidence,
and business results. It does not provide a second task-state engine.

This is the first W05 runtime boundary. It supports keyed submissions, guarded
claims, updates, completion, cancellation, and internal human-task bindings.
The [workflow service](workflow-service.md) now adds definitions, protected APIs,
and an initial interpreter. A separate timeout evaluator checks admitted call
queues and retains its request and reply evidence. Worker registration and
the optional Longhouse adapter remain open.

## Dependency and transport

The client is pinned to Corndogs 0.8.0 commit
`f1b6ec6086dd0341c09629791bfaa959397b3f9c`. Its Go module version is
`v0.0.0-20260930033320-f1b6ec6086dd`. The client is vendored without source changes.
Its [ISC notice](../licenses/corndogs-ISC.txt) comes from that commit's root
`LICENSE.md`. The Go submodule archive does not include that file. Keep the
notice with redistributed source and artifacts.
The [test source](../workflow/recovery_integration_test.go) pins the published
server and PostgreSQL images by digest.

Before a pending operation, `Journal.Check` requires `submission-keys-v1`,
`task-guards-v1`, and both admission policies set to `required`. There is no
legacy fallback. Each journal database is bound to one configured Corndogs
installation. The installation ID identifies its storage authority, not a URL.
Replacement or restored Corndogs storage needs reconciliation before use.

Magi uses the generated client with its own bounded transport. The released
client's stream carrier does not apply the request context to every socket
write. That is unsuitable for this adapter's strict retry deadline.
The Magi carrier limits each complete call to eight seconds, including dial,
TLS, write, and response read. Cancellation closes the socket. It accepts
responses of at most 256 KiB and checks the response ID, variant, and typed body.

Only configured seeds can receive requests. A leader redirect cannot add a
network destination. TLS is required except for literal loopback addresses.
The carrier does not resend an uncertain write. The journal first checks its
original receipt on the next attempt. No transport or key provides Corndogs
client authentication. Keep the server on a restricted service network.

Corndogs uses integer nanosecond timestamps. Its Go adapter permits exact
64-bit integers with bounded CBOR decoding. This does not change Magi's native
wire profile or permit unsafe JavaScript numbers in native records.

## Intent and result transactions

`PrepareTx` joins an admitted business transaction. Its input contains a logical
intent ID, a supported operation, exact generated request bytes, and provenance.
Provenance identifies the caller application, admission evidence, and cause.
The caller must authenticate and authorize the operation before this call.
These local helpers are not public admission endpoints.

The installation and logical intent ID determine a stable request key. The
journal writes the final request with that key before any network mutation.
The same logical ID with different request or provenance bytes is a conflict.
Two coordinators cannot create different keys for one stored intent.
Use a distinct intent ID for each claim poll and each guarded transition.

The journal uses these recovery states:

| State | Meaning |
| --- | --- |
| pending | No durable local decision about the operation result |
| confirmed | A validated remote receipt or observed empty poll is stored |
| rejected | Corndogs returned a final service error |
| reconcile_required | Automatic mutation is unsafe |

These states describe the operation, not the task. `Read` returns retained
operation evidence. `CurrentClaim` queries the authoritative task and compares
its revision with the historical claim. An old receipt cannot authorize a new
worker. The check is not a lock on an external effect.

`SaveResultTx` retains the business output under the task UUID and exact worker
revision. A completion intent requires that result. Local business effects,
results, and an event outbox entry can share one transaction. The Corndogs
completion is a later commit. A lifecycle event must describe the fact that
its transaction established: a stored result is not yet Corndogs completion.
Preserve secret references in all retained inputs and outputs. The journal
does not detect or remove secret values.

Cancellation changes Corndogs metadata only. It does not stop a process or
destroy an environment. External effects still need destination deduplication,
fencing, or explicit unknown-outcome reconciliation.

## Retry window

Before the first possible mutation, the journal commits database time, the
advertised receipt retention, and a safe deadline. The deadline is the original
time plus retention minus 30 seconds. A retry never extends it.

`Resolve` first checks the original submission key or operation ID. An existing
valid receipt can confirm the operation without another mutation, including
after the local retry window. If no receipt exists, replay is allowed only
inside the original window. The saved request bytes do not change.

The database checks time again after receipt lookup. A backward clock change,
more than five seconds of database/host clock difference, changed retention,
or an expired window stops automatic replay. The network call receives the
remaining bounded duration. Server processing delays and clock operations must
remain within the configured recovery assumptions; this is not a distributed
transaction across receipt expiry.

Corndogs 0.8.0 does not store an empty-claim receipt. Magi stores an observed
empty response locally as a confirmed poll. Reusing that intent does not poll
again. If the reply was lost, the pending intent can retry within its window.
A later successful claim has the normal Corndogs operation receipt.

Never restore a journal and start its sender without comparing it with Corndogs
and the effect stores. An absent local intent does not prove that a remote
task or effect is absent. Automated journal-restore reconciliation is not yet
implemented. The event service's generation fence protects its own store only.

## Human assignments

`HumanBinding` stores an assignment service, exact task reference, assignment
generation, waiting Corndogs revision, and stable completion intent.
`BindHumanTx` must join the transaction that retains the assignment creation
receipt. It requires a parked `waiting_human` task with no timeout.
Submit `timeout=-1` to Corndogs for this state. Corndogs stores and reports zero
after it applies that request. Do not submit zero, which selects its default.

After source authentication and completion-policy checks, `CompleteHumanTx`
checks the source application, task reference, and assignment generation.
It stores the source revision, completer, full evidence reference, and result.
The result and guarded completion intent commit together. Duplicate completion
does not create another intent. Different content conflicts. A stale waiting
revision cannot complete a changed Corndogs task.

The tests use synthetic assignment records and real Corndogs. They do not claim
Longhouse authentication or event integration. The
[Longhouse integration request](longhouse-workflow-contract.md) describes the
missing source-service guarantees. Do not use its legacy create operation for
unattended assignment retries.

## Verification and local use

Run `npm run test:workflows` to record three passes with race detection and vet.
Each pass uses a file-backed Corndogs server and two Corndogs servers over one
PostgreSQL store. Magi journal databases remain separate from Corndogs storage.
All identities, credentials, inputs, and results are synthetic.

The matrix tests concurrent submission, lost replies, retained request keys,
expiry refusal, clock reversal, retention change, same-state reassignment,
stale completion, empty polls, and exact-result requirements. Child processes
receive SIGKILL after intent commit, remote acceptance, and result/outbox commit.
Corndogs and PostgreSQL also restart after SIGKILL. Human-binding tests cover
source and generation mismatch, rollback, duplicate completion, and conflicts.

The event bridge test commits one local effect, result, completion intent, and
outbox record. It loses the completion reply and publication reply. Recovery
retains one event obligation and one consumer effect. This is bounded local
effect evidence, not proof that arbitrary remote commands execute exactly once.

The [final report](evidence/index.md#evidence-006)
records 54 leaf checks per pass across three passes. Race detection, vet, and
source-drift checks pass. The report excludes the inert crash-helper entry point.
The [main regression](evidence/index.md#evidence-005)
also passes three times, with 87 Go leaf checks and 382 Node checks per pass.

An [earlier repeated run](evidence/index.md#evidence-004)
found a coordinator race. One replica could confirm between another replica's
receipt lookup and send check. The latter reported a false reconciliation error.
It now returns the confirmed result. Six focused repeated runs and the final
three complete runs pass. The original failure remains in the archive.

## Next Actions

Extend the initial call, human, and finish interpreter over this journal.
Add a worker registry. Monitor the timeout evaluator's readiness. Keep unavailable
automated workers distinct from human work. Add the optional Longhouse adapter
only with its resource-policy checks. Test restore
across the journal, Corndogs, inboxes, outboxes, and external effect authorities.
