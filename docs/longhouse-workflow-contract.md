# Longhouse workflow integration request

This optional integration needs an assignment API that can recover a lost reply without creating a
second task. Longhouse must own assignment authorization and task data. LinkKeys
identifies the caller; it does not grant permission to change a task.

The workflow service owns definitions and runs. Longhouse does not own workflows.
A human step can accept a direct response or use another assignment service.
The requirements below apply only when a workflow creates an assignment in
Longhouse. They are not prerequisites for the workflow interpreter or editor.

The inspected Longhouse checkout is commit
`03b824c008572f56524fe030ce1ea155e8cae022`. Its working tree was clean.
`api/internal/csilservices/task.go` constructs a new task for `CreateTask`.
It does not use the incoming task ID as a creation key. The store inserts it
with a generated ID. `UpdateTask` has no expected-revision guard. The model has
an update timestamp, but no task revision for this protocol.

`CreatePersonalTask` calls `CreateTask`, then adds the task to a private project.
Those operations do not share one store transaction. The new integration must
not expose a workflow's private data between creation and private placement.
Do not implement the adapter as legacy creation followed by a visibility update.

## Additive operations

Keep existing clients compatible. Add typed operations for these capabilities:

| Operation | Required result |
| --- | --- |
| Create workflow assignment | Atomically retain the task, private containment, assignees, external binding, creation receipt, and publication obligation |
| Read creation receipt | Find the original result by authenticated caller, resource scope, and creation key |
| Read bound assignment | Return the current task revision and workflow binding under current read policy |
| Read completion evidence | Return an authorized immutable completion record, not only current open/closed state |

The exact service and operation names can follow Longhouse conventions. Keep
these records separate from calendar extraction work. Use CSIL for the public
contract and preserve integer, timestamp, and privacy semantics.

## Creation and privacy

Namespace the creation key by the authenticated application or principal and
the target house or project. Do not trust a caller-supplied identity field.
The same key and request return the original receipt and task ID. Changed
request content returns a conflict. Retain the request digest and comparison
rules. If keys can expire, advertise retention and provide an explicit recovery
rule; never silently permit a duplicate workflow assignment after expiry.

Bind the assignment to an external installation, run, logical step, and waiting
generation. These are references, not authorization grants. Check the caller's
current resource permissions before creation. Receipt recovery must not release
task content to a caller that lost read permission.

Select the target privacy scope before insertion. Apply private containment,
visibility, assignees, external binding, creation receipt, and event outbox in
one transaction. Roll back all of them on failure. Do not publish a less
restricted intermediate record. Inherit source restrictions in event payloads.

## Completion and revision

A completion record must identify the authoritative service, assignment,
binding generation, task revision, authenticated completer, completion time,
result, and evidence reference. Store it with the task change and a local
outbox entry. A process crash cannot lose the publication obligation.

A worker that accepts the event must authenticate its source and apply local
completion policy. It must match Magi's current human binding. Duplicate
delivery advances the bound step once. Changed content under one event ID
conflicts. An old assignment generation cannot complete a replacement step.

Reassignment, cancellation, deletion, and reopening are separate events. A
reopened task does not rewind a completed workflow. Define expected-revision
guards for new workflow mutation operations. Legacy clients must not bypass
the integrity rules for workflow-bound assignments. The migration can require
guarded operations for those assignments without changing ordinary legacy tasks.

## Acceptance tests

Test lost creation replies, concurrent creation, key conflicts, and database
restart. Prove that private content is never readable during partial creation.
Kill the service after the task transaction and before event publication.
Repeat completion deliveries after Magi restart. Reject source, task, and
generation substitutions. Test withdrawal of read or completion permission.
Repeat the flow across independent LinkKeys and resource-service domains.

## Next Actions

Review and implement these guarantees in the Longhouse repository before
enabling automatic assignment creation in Longhouse. Magi's internal binding tests
can proceed with synthetic records. They do not replace the source-service
acceptance tests. No Longhouse source or deployment was changed for this request.
