# Workflows and task states

Status: an initial independent workflow service and interpreter are implemented.
See the [service guide](workflow-service.md) for supported behavior and limits.
The [operation journal](workflow-journal.md) supplies Corndogs recovery.
Temporal is excluded. Longhouse is an optional assignment integration.
The separate coordinator recovers admitted transitions in the background.
It does not dispatch tools or supply human responses.

## Ownership

The workflow service owns definitions, revisions, run records, and execution
rules. A workflow does not need a project, task manager, persona, or conversation.
Magi coordinates this service with its other components. External applications
can use the same API under LinkKeys authentication and local resource policy.

Longhouse owns its projects and assigned tasks. It can start a workflow or receive
an assignment from one. It does not own the workflow definition or interpreter.
A human response can come through another approved application without a
Longhouse task. The assignment API gap blocks only that optional integration.

## Definition model

A definition has a stable ID, immutable revision, input/output schemas, step
IDs, connections, expression dialect, and required extensions. Each execution
pins that revision. Store editor positions separately from execution semantics.

Start with this proposed subset:

| Step | Meaning in Magi |
| --- | --- |
| Call | Invoke a typed service operation through an authorized adapter |
| Compute | Run commands in an Magi environment, or use a separate inference adapter, with typed inputs and outputs |
| Human | Wait for an authorized response; optionally create a task through an assignment adapter |
| Wait | Wait for a durable deadline or an admitted event |
| Branch | Evaluate a side-effect-free expression over declared inputs |
| Fork and join | Start independent child tasks and apply an explicit join rule |
| Finish or fail | Record a terminal result |

The implemented subset has call, human, and finish steps. All dependencies must
complete before a step starts. Branches, event waits, and loops remain proposed.
The service rejects unsupported step kinds. A flowchart must not imply support
for execution rules that the interpreter does not implement.

Each definition declares typed run inputs and outputs. Each step declares typed
ports and data connections. A data connection does not imply an execution
dependency; the validator checks both. Prompt templates are optional step
configuration. They substitute declared text inputs, not code or secret values.
Their revision and content are part of the immutable definition.

A definition is a reusable process. A run supplies particular inputs and current
authority. A step attempt has its own result and recovery evidence. Sharing a
definition across domains does not share credentials or grant permissions.

The Open Workflow specification supplies constructs for calls, branches,
parallel work, waits, and events. Pin a version and document the exact mapping.
Human assignments and CSIL calls require an explicit extension or composition.
Do not present this proposed subset as a conformant implementation yet.
[Specification](https://github.com/open-workflow-specification/specification/blob/main/dsl-reference.md)

Reject unknown required constructs. Validate graph references, reachable
terminal states, input types, output bindings, branch defaults, and resource
limits. Initially reject cycles. Add bounded iteration only when its execution
identity and cancellation behavior are defined.

Expressions receive declared values and explicit time inputs. They cannot
perform network calls or mutate resources. CEL is a candidate, not a selected
Open Workflow expression dialect. Imported expressions must preserve meaning
or produce a conversion error. Do not translate syntax alone.

## Runtime records

| Record | Owner and purpose |
| --- | --- |
| DefinitionRevision | Workflow service; immutable validated definition |
| RunManifest | Workflow service; definition reference, actor, owner scope, inputs, and causal links; Magi can add session links |
| StepTask | Corndogs; current executable or waiting state |
| TransitionIntent | Magi adapter; operation ID, expected attempt, desired transition, recovery status |
| AttemptReceipt | Magi adapter; accepted output or external operation reference |
| HumanBinding | Workflow service; step reference and optional external assignment identity |
| PlanRevision | Magi; admitted changes to a run, author, prior revision, affected commands |
| EnvironmentUse | Magi; link between a task attempt and its environment, separate from human holds |
| InvestigationRecord | Magi; failure evidence, tested assumptions, retained resources, and next action |

The interpreter is the only writer of workflow transitions through the adapter.
Workers submit results to it. Clients do not edit Corndogs states directly.
Magi can cache task states for queries, but these are labeled observations.
An admitted plan revision does not rewrite the immutable workflow definition.
It records changes to this run and the treatment of commands already in flight.
New access needs new authorization. Preserve the previous plan and its evidence.

Workflow, step, and attempt IDs are internal records, not LinkKeys principals.
Magi agent workers authenticate under the responsible persona. System queries can
use their own authority without a persona or represented human. A session covers
one task and its child work. A new task starts a separate session even within the
same conversation. Private input in one step restricts the session and further
analysis. See [Personas sessions and trust](personas-sessions-and-trust.md).

Changing attention can return to an existing task. A single interaction can
advance several sessions through separately admitted inputs. Preserve the
source interaction and each delivery; do not merge workflow state or replay
effects when a later assessment changes the interpretation.

## Task lifecycle

```text
created -> ready -> running -> succeeded
                 -> blocked_no_worker
running -> retry_due -> ready
running -> reconcile_required | failed
created -> waiting_human -> succeeded | failed
created -> waiting_event/timer -> ready
nonterminal -> cancellation_requested -> cancelled
```

These are semantic states. Their exact Corndogs state strings and timeout
configuration belong to the adapter. Confirm how unclaimed waiting states
avoid timeout transitions before implementation. Business deadlines are not
worker lease timeouts.

A human state deliberately has no execution worker. A missing automated worker
is `blocked_no_worker`. Both remain observable and can have escalation timers.
Maintain Corndogs timeout sweeping as an explicit operational responsibility.
The implemented evaluator checks only admitted call queues. Its readiness gates
new tool calls. See the [service guide](workflow-service.md#timeout-evaluation).
A human can also retain an execution environment while the task waits or after
it ends. This hold is not a Corndogs worker claim or a Longhouse assignment.

## Corndogs recovery gate

The [0.8.0 tests](corndogs-recovery-080.md) pass the service-level recovery
requirements. Keyed submissions recover lost replies. Guarded mutations reject
old revisions, including after the same state name returns. Task and receipt
recovery passed full process restart and the tested multi-node failures.

Use the [selected adapter contract](corndogs-adapter.md). Persist logical step
keys and operation IDs before sending. Set `guarded=true` and require both
admission policies for the dedicated Magi service. Retain the original request
for replay. Do not use legacy completion or update as a comparison guard.

Corndogs receipt retention defaults to one hour. After expiry, the same key
can create another task. Store a safe retry deadline and require reconciliation
after it. Corndogs task revisions do not prevent duplicate external effects.

End-to-end T08/T09 remain open. W05 must test crashes between the Magi intent,
Corndogs transition, business result, and event publication commits. Store
business results separately from Corndogs terminal metadata. Do not add a
second task-state engine or treat a recovered task receipt as an effect receipt.

## Human work

The initial service accepts a typed response for an exact waiting task ID and
revision. It checks the authenticated caller's completion permission. The response
includes an evidence reference. A duplicate response does not advance the step
twice. A changed result or stale task revision is a conflict.

Use an external assignment when a tracked human task is useful. For Longhouse,
create the task with a stable external step reference. Reuse the creation key
on retry. Retain its creation receipt and task identity before binding the step.
The current Longhouse gap and required privacy transaction are in the
[Longhouse integration request](longhouse-workflow-contract.md).
These requirements do not block direct responses or another compliant adapter.

A completion event must identify the authoritative task, revision, completer,
and result. The interpreter verifies that it matches the current HumanBinding
and that local policy permits this completion. Duplicate completion advances
the workflow once. Task deletion, reassignment, and cancellation are separate
events. Reopening a completed task does not silently rewind the workflow.

## Branches, retries, and cancellation

Give each branch and each iteration its own step identity. A join declares
whether it needs all results, any success, or a defined subset. Cancel losing
branches only under an explicit rule. Branch outputs have separate names;
conflicting writes to one output are rejected.

Retry transient transport failures within a budget. Do not retry permission
denials, schema errors, or unknown external outcomes as if they were transient.
Record backoff deadlines durably. A manual retry creates a new attempt with
current authorization. A historical replay has effects disabled by default.

Magi investigates failures and verifies assumptions against evidence. It decides
which resources to retain, what further work is justified, and when a human is
needed. Keep these decisions within task authority and budgets. Preserve the
failed attempt while investigation or a new attempt proceeds. This applies to
service, household, and media tasks as well as code tasks.

Joining an environment does not pause an agent or cancel any command. Pause
and resume control agent work independently from command cancellation. An
explicit run cancellation can still propagate under the rule below.

Cancellation first stops admission of new work. Propagate cancellation to
children and active Magi commands. Record late results without advancing a
cancelled run. Do not destroy a human-held environment as an implicit part of
task cancellation. Release and forced termination follow the
[environment contract](execution-environments.md).
Compensation is a new authorized operation and can itself fail.

## Editor and APIs

The API provides draft save, validation, immutable publication, and run inspection.
The local diagram editor uses the same typed definitions and Go validator.
It imports and exports CSIL draft files. It does not yet log in to the service,
save remote drafts, or execute workflows. Simulation remains proposed.
It displays errors at the affected step and shows each branch's input/output
types. Import/export reports unsupported extensions and conversion losses.

Run inspection shows definition revision, current observed state, attempt,
worker or assignee, elapsed wait, policy result, and causal records. Provide
filtered graph traversal and pagination instead of loading every run at once.

## Next Actions

Connect the editor to authenticated draft APIs. Add worker registration
and explicit reconciliation for uncertain calls.
Complete source-policy processing and restore tests before public deployment.
Implement Longhouse assignment creation only when that integration is required.
