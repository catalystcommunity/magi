# Corndogs adapter contract

Status: selected contract with an implemented operation journal and an initial
[independent workflow interpreter](workflow-service.md).
See the [journal guide](workflow-journal.md) for recovery limits.
Corndogs 0.8.0 supplies the required submission keys and task revisions.
Magi must also recover its own operation records and external effects.

Corndogs owns task state. The workflow service owns definitions, transition intents,
execution links, and business results. Do not create a second editable task
status in Magi.

## Service requirements

Use these settings for a new Magi task service:

```text
CORNDOGS_SUBMISSION_KEY_POLICY=required
CORNDOGS_TASK_GUARD_POLICY=required
```

Corndogs and its chart default to `compatibility`. Set both values explicitly.
An existing shared service can retain compatibility during a client upgrade.
Every Magi submission must still set `guarded=true`.

Before admission, call `GetServerInfo`. Require `submission-keys-v1` and
`task-guards-v1`. Check the receipt retention and both policies. For a dedicated
Magi service, fail readiness if either policy is not `required`.
Never retry an unsupported operation through a legacy operation.

All servers that can write to the store must support the contract. Complete
the server upgrade before admitting protected work. A mixed file cluster is
not supported. An old PostgreSQL server can bypass the new guards.
Follow the [Corndogs upgrade procedure](../../corndogs/docs/resilience.md#upgrade).

Corndogs keys and revisions are not identity or authorization evidence.
Restrict access to its mutation API to authorized service components.
An expected revision prevents a stale write; it does not prove caller authority.

## Operation mapping

| Magi action | Corndogs operation | Rule |
| --- | --- | --- |
| Admit one logical step | `SubmitKeyedTask` | Store a key before sending. Set `guarded=true`. |
| Find an uncertain submission | `LookupSubmission` | Use the original queue and key. Do not infer absence from a follower read. |
| Assign work | `ClaimGuardedTask` or `ClaimGuardedTaskGroup` | Store an operation ID before sending. Bind the worker to the returned revision. |
| Advance or park a step | `UpdateGuardedTask` | Send the expected revision and state. |
| Finish a step | `CompleteGuardedTask` | Store the business result before completing the task. |
| Cancel a step | `CancelGuardedTask` | A successful task cancellation does not stop an external process. |
| Read current task state | `GetGuardedTask` | Use this read for recovery. It includes terminal metadata and revision. |
| Find an uncertain mutation | `LookupOperation` | Use the original operation ID. |
| Release timed out claims | `CleanUpTimedOut` | Assign an explicit owner and monitor sweep progress. |

Use legacy metric reads for observation only. They can read a stale local
copy in a file cluster. Recovery reads must follow leader redirects.

## Durable intent records

Before each mutation, commit its intent to Magi storage. Use a unique logical
step reference to prevent two interpreters from creating different keys for
the same step. Use a separate unique ID for each intended operation.

The record needs these fields:

- Receiving Corndogs installation, queue, and logical step reference.
- Operation name, stable key or operation ID, and exact request fields.
- Task UUID and expected revision when known.
- First possible send time, retention setting, and last safe retry time.
- Initiating identity, execution attempt, cause, and authorization reference.
- Receipt, result reference, and recovery status when known.

Submission keys belong to a queue in one store. Operation IDs belong to the
store, not to a queue. Include the receiving installation in Magi records.
Generate IDs once. A restart, new connection, or leader change must not replace
them. Preserve omitted fields and payload bytes on replay.

Recovery status describes the intent, not the task. For example, an intent
can be pending, confirmed, rejected, or require reconciliation while Corndogs
continues to own the actual task state.

## Submission recovery

1. Commit the unique step intent and original keyed request.
2. Set a request deadline within the safe retry period. Send the request.
3. Commit the returned receipt and task UUID before acknowledging admission.
4. After a crash or uncertain reply, read the intent and call
   `LookupSubmission` with its original queue and key.
5. If a receipt exists, verify the binding and commit the recovered receipt.
6. If no receipt exists and the retry period remains valid, resend the same
   request. A concurrent first request must resolve to the same receipt.
7. If the retry period has ended, require reconciliation. Do not create a new
   key or resend the old key automatically.

An uncertain lookup does not prove absence. Stop and retry the read within
policy. A submission conflict is an error in request binding or key reuse.
Do not change the request to bypass the conflict.

## Receipt retention

Corndogs defaults to one hour. After expiry, the same submission key can
create another task. A missing receipt then means only that no unexpired
receipt exists. The original task can still exist or have completed.

Choose retention from the maximum recovery delay, request duration, and clock
uncertainty. Apply it to every server. Do not reduce retention while unresolved
intents depend on the longer period. Select and test a value before unattended
deployment; this contract does not select a production duration.

Calculate the safe retry deadline from the first possible send time, not
from the latest retry. Subtract the allowed clock and request margin. Persist
this deadline before sending. After receipt recovery, also honor its expiry.
Every network call, including automatic transport retries, must finish before
the safe deadline. A transport attempt count alone does not establish this.

If the clock, retention configuration, or backup history is uncertain, require
reconciliation. Never infer that a missing receipt after expiry permits a new
business operation. Keep Magi's intent and result records longer than Corndogs
receipts as required by the workflow and audit policy.

A domain outage does not require sending work to Corndogs immediately. An
unsent durable event can wait in Magi's outbox. Its Corndogs receipt period
starts only after submission. This distinction does not remove the retention
limit from requests that might already have been sent.

## Worker and transition recovery

Bind each execution to the task UUID and claim revision. A timeout sweep and
new claim increase that revision, even when the state name stays the same.
Do not replace the expected revision in an old worker result with a fresh one.

For a guarded mutation, store the intent, send it, then store its receipt.
After an uncertain reply, query its operation ID. Within the retry period,
the same operation ID and request can recover the first result.
Check the current task separately before admitting subsequent work: a replay
can describe an earlier successful mutation, not the current state.

Treat these outcomes explicitly:

| Outcome | Adapter action |
| --- | --- |
| Revision or expected-state conflict | Reject the old result. Read current state to explain the conflict. |
| Claim superseded | Do not start work from that replay. Reconcile the old execution. |
| Key or operation ID conflict | Stop automatic retry. Report inconsistent intent data. |
| Request not applied | Retry the same intent only within its deadline and policy. |
| Outcome uncertain | Query the receipt or safely replay the same protected request. |
| Unsupported feature | Fail readiness. Do not remove protection. |
| Receipt expired or recovery evidence missing | Require reconciliation before another effect. |

Use a separate operation ID for the next claim poll after an empty result.
Repeating the old ID is a replay, not a request for newly available work.

Human waits need no claim worker. Park the task with no worker lease timeout.
Bind a direct response or optional external assignment to the step and intended
waiting revision. On completion, validate that binding and use a guarded transition.
A fresh task read alone must not make an old human completion valid.

## External effects and publication

A Corndogs revision cannot stop an already running external process.
Use a stable operation key and reconciliation at the destination service.
If the destination cannot deduplicate or report the outcome, keep the result
uncertain. Do not infer failure from a lost reply.

Commit the business result or external receipt before completing the task.
Publish lifecycle events from a durable publication record. Preserve the
causal link between the result, Corndogs transition, and publication.
Corndogs terminal metadata does not store the business output.

The Magi and Corndogs commits are separate transactions. The durable intent
connects them. A process must recover either side without a new task key,
duplicate effect, or second authoritative task status.

## Next Actions

The [journal implementation](workflow-journal.md) now tests exact intents,
lost replies, process termination, stale revisions, local retry-window expiry,
clock reversal, retention changes, and result/outbox recovery. The initial
interpreter adds direct human responses and call dispatch. The separate timeout
evaluator now checks admitted call queues. Monitor its readiness and test
coordinated backup restore before unattended deployment.
Test Longhouse events separately if that optional adapter is enabled.
