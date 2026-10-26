# Independent workflow service

The workflow service owns reusable definitions and their runs. Longhouse is
optional. A workflow does not require a project, persona, or conversation.
The initial interpreter uses the tested Corndogs journal for task transitions.
This is a local development prototype. It is not deployed to the public lab.

## Component contract

The standalone entry point is `csil/workflows.csil`. The aggregate Magi schema
includes the same component. Both generate Go and TypeScript clients.
The standalone schema contains only shared records, Discovery, and Workflows.
Existing service and operation wire IDs are unchanged.

| Operation | Signed body | Result |
| --- | --- | --- |
| validate | WorkflowDefinition | Validation issues with record paths |
| save-draft | SaveWorkflowDraft | Draft with a new edit version |
| read-draft | ReadWorkflowDraft | Current draft |
| publish | WorkflowDefinition | Immutable definition reference and digest |
| read-definition | ReadWorkflowDefinition | Exact published definition |
| start | StartWorkflow | Original run admission receipt |
| read-run | ReadWorkflowRun | Retained task observations and available results |
| complete-human | CompleteWorkflowHuman | Receipt for the accepted response |
| read-call | ReadWorkflowCall | Retained call request, response, and outcome evidence |

Discovery is public. All workflow operations require a current LinkKeys
application proof. The signature binds the operation, component schema, caller,
receiver, and exact input bytes. The shared `identity` package resolves keys
against configured domains and independent domain-key pins. The event service
uses the same resolver. Neither service requires the other to be deployed.

Local grants match an exact application, owner scope, and action. Actions are
`read`, `write`, `run`, `complete`, and `execute`. There are no implicit grants
for an owner name, persona, identity domain, or imported definition. Grant changes
serialize with local admission transactions. Receiving services still authorize
their own operations. LinkKeys authenticates; it does not decide resource access.

The public endpoint retains the first signed publication, run request, and human
response. Local Go helpers require a trusted caller that has already authenticated
the actor. Do not expose those helpers as unauthenticated HTTP handlers.
An application signature does not prove human approval of a delegation allowance.

## Definitions and drafts

A definition has an owner scope, ID, immutable revision, typed ports, steps, and
result connections. A run pins its revision and digest. Publishing the same
revision and content returns the original reference. Changed content conflicts.

A draft has a separate edit version. Save requires the expected version.
A repeated last save from the same application returns the saved draft. A stale
save cannot replace a newer edit. Editor coordinates are separate from the
definition. Moving a node cannot change its execution digest.

The initial subset has these limits:

- At most 16 steps, with at most 16 input and output ports per record.
- At most 32 KiB per definition or draft request.
- At most 4 KiB per input or result value set.
- Values are text, integer, boolean, decimal, timestamp, or bytes.
- All declared dependencies must complete. Cycles and unknown step kinds fail validation.
- One finish step must depend on all other steps, directly or indirectly.

The native wire profile preserves exact decimals. Integers use its JavaScript-safe
bound. Timestamps have millisecond precision. There is no conversion through
floating-point JSON. Structured application values and artifact ports need a
later typed extension; the initial scalar ports are not a universal data model.

Timestamp values use `WorkflowTimestampValue`, a named record with an `at`
field. CSILgen 0.2.9 emits an unconditional TypeScript guard for a bare timestamp
union arm. That guard incorrectly selects a later byte value. The named record
avoids this generator issue without changing existing protocol types.
The [failed check](evidence/index.md#evidence-008)
is retained. No shared CSILgen source or generated file was manually patched.

Data connections and execution dependencies are separate. A step can read a
run input or an ancestor's output. The source and destination port types must
match. Finish passes its declared inputs to outputs with the same names and types.
Run outputs select declared values after all steps complete.

An optional prompt contains a revision and template. `{{input-name}}` substitutes
a declared text input once. Values do not undergo a second template expansion.
Templates cannot run code, read files, fetch URLs, or resolve secrets. The full
template is pinned in the definition. A rendered prompt is retained with the
step input. It does not cause a model call unless an approved call adapter uses it.

This subset is not an Open Workflow Specification conformance claim. The broader
[workflow design](workflows.md) still includes proposed branches, waits, and loops.

## Execution and recovery

`Store.Advance` finds ready steps and records their exact inputs, rendered prompt,
and Corndogs submission intent in one local transaction. It then resolves remote
transitions outside the transaction. Repeated calls use stable operation keys.
It does not invoke external capabilities.

`Engine.Execute` selects an installed call adapter. It checks execution permission,
obtains a guarded Corndogs claim, and checks the current claim revision. Before
calling the adapter, it commits a dispatch record. The adapter receives the
actor, pinned definition, run and step IDs, effect key, inputs, and prompt.
The adapter must enforce destination authorization and its own effect rules.

A successful result, evidence reference, and completion intent commit together.
Downstream work uses the result only after a confirmed Corndogs completion.
A lost completion reply recovers through the original journal key.
Corndogs remains the task-state authority. API phases describe retained evidence;
they are not independently editable task states or a live worker heartbeat.

A failed call or crash after dispatch leaves `outcome_unknown`. The engine does
not repeat it. A blocked adapter can still have an external effect after its
caller times out. Call adapters must honor cancellation and bound their work.
The default call deadline is eight seconds; its Corndogs claim is thirty seconds.
An operator can set a whole-second call deadline from one to 120 seconds.
The claim uses that deadline plus 22 seconds. A workflow cannot change this
setting. A changed setting does not extend an existing claim.
Claim expiry targets reconciliation, not automatic effect replay.
New calls also require a current timeout evaluator. The engine checks this
before claim admission and before the dispatch record. It refuses claims with
the configured call deadline plus five seconds or less remaining. This reserves
the full call budget and five seconds for clock differences.

A human step parks a guarded task without a timeout. A direct response must
match the task ID and revision, pass completion policy, and supply typed outputs
and an evidence reference. An identical response is idempotent. Changed content,
another completer, or a stale revision conflicts. Longhouse is not involved.
An optional assignment adapter can use the existing human-binding journal.

## Background coordination

Run admission and its scheduling record commit in one transaction. A separate
coordinator selects due runs in bounded batches. Multiple replicas can select
different runs with `FOR UPDATE SKIP LOCKED`. Each claim has a generation and a
sixty-second lease. Database time determines due times and lease expiry.
The coordinator holds no database transaction during a network call.

Each evaluation has a thirty-second deadline. A late or expired claim cannot
replace the scheduling result of a newer claim. If a process stops, another
replica can select the run after its lease expires. The operation journal and
Corndogs guards still protect remote transitions. A scheduling lease is not
permission to repeat an external effect or resume a restored journal.
Database acquisition and scheduling updates each have a five-second deadline.

The coordinator calls `Store.Advance`. It has no tool adapters. Call steps remain
`ready` until an approved executor acts. Human steps remain `waiting_human` until
an authorized response arrives. A missing worker is not a human assignment.
Results with an unknown outcome do not cause another call.

A stored result requests a coordination wake in the same transaction. An active
coordinator cannot replace that wake with an older unknown-outcome backoff.
This reduces the delay between a model response and the completed run.

The default coordinator batch limit is sixteen runs. The process waits two
seconds between batches. An evaluated run becomes due after two seconds.
Errors, denied access, and reconciliation use a thirty-second retry interval.
These are poll intervals, not response-time guarantees under load. Each
evaluation checks the original actor's current run grant. Restored permission
permits later work; the coordinator does not create a grant.

Only a snapshot with all confirmed step completions removes a run from polling.
An exact start retry does not reset a lease or reopen completed work. The
`workflow_coordination` table stores due times, generations, and bounded outcome
codes. These records describe coordination, not a second task-state authority.
Process logs contain counts and generic errors, not inputs or provider errors.

## Installed question worker

The [question and answer setup](question-answer.md) installs one Python model
adapter. `workflow.Worker` selects a pinned definition in one owner scope.
The question worker also selects one exact requesting application. It checks
current execution grants before dispatch. Replicas use the engine's existing
dispatch and Corndogs guards; selection itself grants no execution authority.
The bounded cursor passes unrelated definitions instead of selecting them.

This is operator configuration, not remote worker registration. Arbitrary shell
commands, model-selected tools, and credentialed model calls are not supported.
The local test launcher combines process roles for setup only. The normal
service can run its worker separately with `-question-worker`.

`read-call` uses the current `read` grant. Request evidence is stored before
the adapter starts its model request. Available response evidence is stored
before the workflow result. A prepared record is not proof of delivery.
A returned response is not proof that the workflow completed. Evidence can
remain after an execution failure. Reading it does not repeat the call.

Evidence payloads have a media type and adapter revision. Requests have a 16 KiB
limit; responses have a 64 KiB limit. These records accept sanitized payloads
from trusted adapters. They do not implement secret substitution. The current
model adapter therefore accepts no credentials and is limited to test data.

## Timeout evaluation

Corndogs applies timeouts only when `CleanUpTimedOut` runs. The separate
`-sweep` process owns this duty for the workflow service. A call's claim intent
and timeout work commit together. With the default call budget, the initial
timeout check is due thirty-five seconds after claim admission. With another
budget, it uses the claim duration plus five seconds. The evaluator never
submits a claim or calls a
tool. Human steps have no timeout work and remain parked.

Each request selects one queue derived from an admitted call step. The evaluator
does not use an empty queue or enumerate all Corndogs queues. Use a dedicated
Corndogs installation for each workflow store. The current queue keys do not
isolate two workflow stores with the same owner and run IDs on one backend.
Corndogs network access must remain restricted to trusted service processes.

Replicas use expiring leases and generations. Each claim stores the exact
timeout request before sending it. The request time is five seconds behind the
database clock. Database and Corndogs clocks must stay within five seconds of
one another. The evaluator checks its local clock against the database clock;
it does not measure Corndogs clock accuracy. Monitor clock synchronization.

Each attempt retains the available reply, task observation, and outcome. A lost
reply or process failure can leave a pending attempt. A later attempt reads the
same task. It does not infer an effect count from a zero timeout count. An old
lease can retain a late reply but cannot replace a newer scheduling result.
These records are evidence, not independently editable Corndogs task states.

An expired call moves to `reconcile_required`. Its timeout becomes zero, so
repeated evaluation does not release it again. A dispatched call with no result
still has `outcome_unknown`. The evaluator does not repeat it. A terminal task
or a confirmed reconciliation state ends timeout polling for that call.
A timeout does not stop a process or revoke its credentials. An expired claim
does not prove that the external effect did not occur.

The process checks at most sixteen queues per pass, with a thirty-second pass
deadline. Each network evaluation has a ten-second deadline. Database operations
have five-second deadlines. Active or failed checks become due after five
seconds; the loop waits two seconds between passes.

`-sweep-check` fails if the heartbeat is absent, more than fifteen seconds old,
or too far in the future. It also fails for an error record or timeout work more
than fifteen seconds overdue. A heartbeat cannot hide a blocked queue. This
readiness check gates new tool calls across this workflow store. It does not
block API reads, human responses, or coordination. It does not prove that a
specific worker or tool is available.

## Local editor

From the repository root, run:

```sh
npm run dev:workflow-editor
```

Open `http://127.0.0.1:8092`. The editor binds only to literal loopback.
It uses the compiled standalone client records and the Go definition validator.
It has no database, credentials, service proxy, or execution endpoint.

Select a step to edit its ports, dependencies, connections, capability, and
prompt. Use the Apply button before changing steps or saving. Drag nodes to
change their positions. Position fields also support keyboard input.
The solid lines show execution dependencies. Dashed lines show data connections.

Save a draft file to retain both the definition and layout. Export a validated
definition for use with the service API. Import checks the bounded CSIL record
before displaying it. Drafts remain in memory until the browser saves a file.
Removing a step retains broken references so validation can identify repairs.

This editor does not yet authenticate to the workflow service, save remote
drafts, publish revisions, or inspect runs. It is an authoring client, not an
alternative source of execution authority.

## Service process

Build with `go build ./cmd/workflows`. Supply `-config` with the service
application, Corndogs installation ID and seeds, trusted LinkKeys domains,
pinned RP address, and optional grants. Configuration uses the field names in
`cmd/workflows/main.go`.

Set `WORKFLOWS_DATABASE_URL` and `WORKFLOWS_RP_API_KEY` outside the configuration
file. Do not put these values in evidence or shared notes. Use `-migrate` with
an administrator database role, then `-apply-policy` to apply configured grants.
The database binds to one service application and one Corndogs installation.

The service schema is now version four. Stop older API, coordinator, and
executor processes before migration. Migration preserves version-one and
version-two and version-three admissions. It adds missing coordination,
timeout, call-evidence, and result-wake records.
It checks the existing service binding. Repeated migration does not reopen
completed coordination or timeout work. Existing CSIL wire IDs are unchanged.
The new `read-call` operation uses Workflows operation ID eight. Regenerate
clients before use: signatures bind the component schema digest, which changed.

Use a separate runtime database role. It needs reads from the binding and grant
tables. It needs reads and writes for drafts, definitions, runs, steps, and
journal and coordination records. It does not need grant writes, schema changes,
or deletion. For an existing role named `workflow_runtime`, use:

```sql
GRANT USAGE ON SCHEMA public TO workflow_runtime;
GRANT SELECT ON workflow_service, workflow_grants, workflow_journal_binding
  TO workflow_runtime;
GRANT SELECT, INSERT, UPDATE ON workflow_definitions, workflow_drafts,
  workflow_runs, workflow_run_steps, workflow_coordination, workflow_intents,
  workflow_results, workflow_human_bindings, workflow_sweep_health,
  workflow_timeout_work, workflow_timeout_attempts, workflow_call_evidence
  TO workflow_runtime;
```

The integration fixture completes call, human, and finish steps with these
permissions. It verifies that policy edits, migration, deletion, and binding
changes fail. The administrator still creates the role and database. Check the
actual grants when deploying; the fixture does not configure the public lab.

The default listener is `127.0.0.1:8082`. `WORKFLOWS_LISTEN` and
`WORKFLOWS_ORIGIN` configure a service behind TLS. Non-loopback Corndogs needs
TLS with a server name and trusted CA. Corndogs must use a restricted network;
the current Corndogs protocol does not authenticate clients.

The API and coordinator run as separate processes. Start the coordinator with:

```sh
go run ./cmd/workflows -config /path/to/workflows.json -coordinate
```

Use the same service application, database, and Corndogs installation as the API.
This process does not open an HTTP listener or require a LinkKeys RP API key.
It works from previously authenticated admissions and current service policy.
It does not dispatch tools or sweep Corndogs timeouts. Replicas use the same
database and can replace one another after lease expiry.

Run the timeout evaluator as another process with the same configuration:

```sh
go run ./cmd/workflows -config /path/to/workflows.json -sweep
```

Use this local readiness probe before enabling executors:

```sh
go run ./cmd/workflows -config /path/to/workflows.json -sweep-check
```

Both commands use the database and Corndogs connection. Neither opens an HTTP
listener or requires a LinkKeys RP API key. Connect the probe to operational
alerts. Starting the process alone does not prove that it can evaluate timeouts.

Use `-advance RUN -owner-domain DOMAIN -owner-account ACCOUNT -scope SCOPE`
to evaluate one run's transitions. Repeated evaluations can admit newly ready
steps. This command does not dispatch call adapters. The integration tests
install a bounded synthetic adapter through the Go engine API.

To install the question worker behind the normal LinkKeys API, add a
`question_worker` object to the service configuration. It has these fields:

- `actor`: the exact admitted requesting ApplicationRef.
- `owner`: its authorized ScopeRef.
- `model`: `endpoint`, `model`, absolute `python` path, and `max_tokens`.
- `call_seconds`: a whole number from eight to 120.

Use the generated JSON field names for identity records, such as `account_id`,
`application_id`, and `instance_id`. Select the model endpoint and instructions
as operator configuration, not workflow input. Then start another process:

```sh
go run ./cmd/workflows -config /path/to/workflows.json -question-worker
```

Keep the API, coordinator, and timeout evaluator running. The worker needs no
HTTP listener or RP API key. It uses previously authenticated admissions and
current database policy. The CLI profile must contain a currently attested
application signing key, the exact service recipient, and the authorized owner.
The local test-key profile is not a LinkKeys enrollment method.

## Verification

`npm test` checks generation, Go and TypeScript codecs, older wire fixtures,
authentication, the editor boundary, HTTP behavior, race detection, and vet.
`npm run test:workflows` runs the journal and interpreter against file-backed
Corndogs and two Corndogs servers over PostgreSQL. Each command repeats its
matrix three times and retains source hashes and raw output.

The interpreter tests cover immutable publication, draft conflicts, separate
runs, direct human responses, concurrent dispatch, policy withdrawal, and lost
completion replies. A child process receives SIGKILL after a synthetic effect
commits and before its result is stored. Restart refuses to repeat the effect.
That fixture uses a separate transaction in the test database. It is not proof
of recovery for an arbitrary remote business service.

The standalone generated Go client uses the protected HTTP API with synthetic
foreign application keys. Real independent-domain LinkKeys workflow tests remain
open. The event suite checks the shared resolver with real disposable LinkKeys
servers. Public lab services are unchanged.

Coordinator tests cover atomic admission, bounded batches, concurrent claims,
locked rows, stale leases, policy withdrawal, human responses, and lost replies.
They also exhaust the database pool to test its acquisition deadline. A child
process receives SIGKILL after remote transitions complete but before its lease
is released. A replacement recovers the same operations. Tests move the stored
lease deadline to simulate expiry; they do not measure a sixty-second failover.
The restricted-role test covers the interpreter, coordinator, and timeout
evaluator storage paths.

Timeout tests cover atomic claim admission, stale readiness, concurrent replicas,
late replies, lost replies, and process termination. They verify that human
waits and an unrelated Corndogs queue stay unchanged. A claim that expires after
a pre-dispatch crash cannot start the tool. These fixtures use a guarded update
to shorten a claim to one second, then wait for real elapsed time and the
five-second margin. They do not measure the full thirty-second claim timeout.

The [main report](evidence/index.md#evidence-013) records
171 Go leaf checks and 482 Node checks in each of three passes. Generation,
race detection, vet, and source checks pass. The
[browser record](evidence/index.md#evidence-003)
records local editor checks and their limits. Browser download persistence and
pointer dragging were not automated in that check.

The [workflow report](evidence/index.md#evidence-010)
records 178 leaf checks in each of three passes. It excludes the four inactive
crash-helper entry points. Both current reports pass race detection, vet, and
source checks. The
[event report](evidence/index.md#evidence-009)
records 42 Go leaf checks and 23 Node checks per pass, including the Node parent
test. All three event passes include real disposable LinkKeys domains. That
report is from the previous work; event code is unchanged. The report filenames
use UTC; the local work date is 2026-10-14.

Worker checks cover concurrent selection, passing unrelated definitions,
current execution grants, immutable evidence, result wakeups, and migration
from schema version three. The [question guide](question-answer.md) records
the separate Python, actual-model, CLI, and Compose checks.

## Next Actions

The first public Q&A path now uses real LinkKeys authentication. See the
[Example environment guide](question-answer-example.md). It admits one test actor
and scope. It does not authorize arbitrary callers or private data.

1. Connect editor login and remote drafts to the protected API. Keep private keys
   out of document files and browser persistence.
2. Add a worker registry and authenticated worker execution APIs.
   Missing workers must remain distinct from a human wait.
3. Add authorized reconciliation for uncertain calls. Do not clear a dispatch
   record to cause replay. Preserve the original attempt and evidence.
4. Implement source-policy processing before admitting restricted sources.
   The prototype rejects nonempty source restrictions; it does not ignore them.
5. Connect secret-reference capture before running real credentialed tools.
   Current inputs and results are retained as supplied and are not redacted.
6. Test quotas, journal restore, and independent-domain
   workflows before broader deployment. Do not resume a restored journal without
   comparing it with Corndogs and the effect authorities.
7. Add branches, cancellation, plan revisions, and external assignment adapters
   as separate capabilities. Longhouse is one optional adapter, not a prerequisite.
