# jr2 review

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

Review date: 2026-09-27. Status: reference observations only.

Repository: [snapwich/jr2](https://github.com/snapwich/jr2).
Inspected commit: `fc67cff40d44fbceebc25c57d56c40ceb1111f28`.
Local clone: `/tmp/opal-jr2-review.g8x8bj/jr2`.
The clone is temporary. The source links below identify the inspected commit.

No dependencies were installed. No project code, test suite, or deployment
command was run. This review is not a security audit or a recovery test.

## Scope

The user requested ideas, not code reuse. Do not copy code, fork jr2, or adopt
it as a dependency. Magi's vision and requirements remain independent of jr2.
Use a lesson only if it helps implement an existing Magi requirement.

The comparisons below record possible lessons, not new requirements or planned
work. Earlier jr2-derived additions to the core design and work plan were
removed after the user clarified this boundary. The observations remain here
for reference. No further jr2 investigation is planned.

jr2 combines TypeScript/XState workflows, a Go Kubernetes operator, an HTTP
control API, and remote agent containers. Its own agent server uses Pi agent
and inference libraries. The deployed orchestrator uses one writer and SQLite
snapshots. These are coherent choices for jr2, but they are not requirements
for Magi. Its cluster distribution is not Magi's cross-domain authority model.
[Architecture](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/docs/architecture.md),
[agent dependencies](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/harness/package.json),
[snapshot store](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/orchestrator/src/snapshot-store.ts).

## Patterns observed

| jr2 pattern | Possible relevance to existing Magi requirements | Boundary to keep |
| --- | --- | --- |
| Each active state exposes a typed action menu | Generate model tools and client controls from CSIL descriptors | A menu is not permission |
| Human gates and agent actions use separate routes | Bind human completion to Longhouse and receiver policy | An agent cannot approve its own work merely by naming the approval action |
| A turn belongs to the state that requested it | Bind each turn to a step attempt; stop it when that attempt ends | Reject stale results even if cancellation fails |
| A credential helper runs outside the agent container | Keep provider and service secrets in trusted adapters | Secret isolation does not prevent misuse of an allowed operation |
| Reusable workflows leave model and repository choices unbound | Validate required model, image, tool, and resource bindings before admission | Do not select a paid provider through an implicit default |
| One API serves the console, CLI, and integrations | Derive forms and graph views from the same published definition | Clients do not own execution state |
| Current-state feeds replace the full displayed state | Use a separate live observation API for clients | Durable events and audit records still need retention and recovery |

## Typed menus and human gates

`defineEvent` defines a name, input schema, audience, and delivery semantics.
The workflow uses those definitions for validation and discovery. The agent
receives schemas as model tools. In the packaged task workflow, the agent can
report completion. A separate human gate accepts approval or change requests.
[Event definitions](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/agent-protocol/src/define-event.ts),
[task workflow](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/machines/src/task.ts).

This is a useful way to reduce the model's choices without embedding workflow
rules in prompts. For Magi, generate descriptions from the active definition
and CSIL descriptors. Check identity, local resource policy, input values,
current step, and attempt generation when an action arrives.

jr2's menu checks deliberately permit uncertainty when a guard cannot be
evaluated. They predict whether an event can change state; they are not the
token authorization checks. Magi must not use this permissive behavior for
authorization. A policy error must deny the protected operation.
[Registration checks](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/orchestrator/src/registration.ts).

jr2 separates delivery, predicted state movement, and turn completion in its
agent receipt. Magi should also distinguish receipt acceptance, committed
transition, effect completion, and turn termination. Do not report committed
progress from a prediction alone.
[Receipt implementation](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/orchestrator/src/run-host.ts).

## Credential isolation

jr2's Custodian uses Envoy in a separate container. The agent receives public
placeholder values. The helper substitutes a real credential only for a
configured destination. The implementation checks headers and paths and
limits the control routes it forwards.
[Custodian design](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/docs/adr/0059-a-harness-holds-stand-ins-and-the-custodian-holds-the-keys.md),
[request checks](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/orchestrator/src/custodian.lua).

Keep the principle in Magi: code that the agent can change must not hold
unnecessary long-lived credentials. Reactorcide supplies secrets to trusted
components under its existing controls. LinkKeys authenticates callers;
the receiving service still decides which operations it permits.

Do not require transparent TLS interception in the first Magi implementation.
Start with an explicit inference or tool adapter. This also permits Windows
and macOS execution without requiring a Kubernetes sidecar.

The jr2 design identifies important limits. Open network access lets code
bypass proxy routing. Credentials that sign requests need a different adapter.
A live sandbox receives rotated values only after replacement. For Magi,
test network enforcement, credential withdrawal, allowed-operation misuse,
and budget enforcement separately from secret visibility.

## Recovery boundaries

jr2 saves workflow snapshots and agent admission records. When an orchestrator
restarts, a recorded admission lets it attach to an existing turn. The code
also identifies a gap between sending a request and recording its admission.
Without a recorded admission, recovery can submit the prompt again.
[Recovery codec](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/orchestrator/src/durability.ts).

The inspected agent server stores conversations in a process-local map.
Each conversation stores its queue, stream, and history in memory. Do not
treat orchestrator recovery as proof of agent-process recovery or durable
at-least-once delivery.
[Agent server](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/harness/src/app.ts),
[conversation storage](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/harness/src/conversation.ts).

For Magi, keep stable command IDs and durable admission receipts. Test a lost
reply, interpreter restart, worker restart, and stale worker separately.
Record an unknown outcome when the downstream system cannot reconcile it.
Conversation identity, execution attempt, and workspace identity must remain
separate. Keeping a conversation does not keep an execution claim valid.

The packaged jr2 task retains its workspace for human review. Approval ends
the run and removes the workspace. The task does not push its branch; work
that was not exported can be lost. Magi should require a durable artifact
receipt before automatic workspace cleanup. An explicit discard is a
different authorized operation.

## Workflow revisions

jr2 checks a workflow shape digest before restoring a snapshot. A mismatch
stops restoration and preserves the snapshot for inspection. The digest does
not include prompt text, guard bodies, or actions. It checks structural
compatibility, not complete behavioral identity.
[Fingerprint implementation](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/orchestrator/src/fingerprint.ts).

Magi should preserve immutable definition revisions, including executable
bindings and prompt artifact references. A compatible graph alone does not
permit a silent change to an active run. Record explicit migrations.
Continue to evaluate current resource policy when an operation is requested;
an old workflow revision does not preserve withdrawn permission.

## Observation, privacy, and federation

jr2 sends complete current-state observations on connection and later changes.
This lets a dashboard recover its current view without an event replay log.
It does not recover every transition that occurred during disconnection.
Use this pattern for Magi's live displays, not for its durable subscriptions.
[Observation design](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/docs/adr/0022-observation-is-a-level-triggered-feed.md).

jr2 permits anonymous access to reduced workflow observations. Full run state
and control require its instance token. Magi needs service-local policy even
for workflow names, state names, and activity metadata. Do not copy the open
observation default into household or business services.
[HTTP access boundaries](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/packages/orchestrator/src/http.ts).

An Magi action menu from another domain is an authenticated description, not
an authorization grant. The receiving authority validates each command under
its own policy. Keep catalystsquad.local primary and partner.local secondary;
do not share an instance administrator token between them.

## License and verification

The inspected repository has an MIT license. This is a source fact, not a
proposal to reuse code. No code reuse is intended.
[License](https://github.com/snapwich/jr2/blob/fc67cff40d44fbceebc25c57d56c40ceb1111f28/LICENSE).

The repository includes tests for authorization, menus, recovery, and the
credential helper. Those tests are useful sources of test cases. They were
not run during this review. Do not infer passing results from their presence.

## Next Actions

Continue the Magi implementation plan from its own requirements. Consult this
review only when a recorded lesson can reduce work on an existing task.
