# Integrated Magi release design

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. See the
[configuration overlay](../config/README.md).

Updated 2026-10-16. This is the broader design for one connected test release.
It brings together approved requirements, proposed mechanisms, and existing
service experiments. The [demo prototype design](demo-prototype.md) records
the new speech, harness, and conversation-graph requirements. Those design gates
remain open. A passing component test is not completion of this release.

The release must support useful work from an authenticated interaction through
context selection, model and tool work, human participation, and retained
evidence. It must recover after failure. An external application must also be
able to use each permitted component without adopting the whole Magi system.

The user selected Android first, with both a regular application mode and an
optional default-assistant mode. Web and CLI clients remain supported.

## Release scope

Implement these functions as one connected system:

- Stable personas, authorized representation, task sessions, and concurrent
  conversations across text and voice.
- Registered models, hosts, tools, source adapters, and interaction points.
- Service-local policy, source restrictions, processor trust, and exact human
  approval of allowances.
- A Python agent runtime with model adapters, tool proposals, independent
  evaluation, bounded work, and explicit human requests.
- Native, container, Windows VM, and macOS VM execution through host executors.
  Kubernetes is one optional placement mechanism.
- Reference-only evidence capture, task history, original interactions, later
  interpretations, and the actual input used for each operation.
- Knowledge authoring, scoped acceptance, permitted retrieval, live source
  queries, origin-controlled mirrors, invalidation, and deletion.
- Reusable workflows, a graph editor, durable events, a personal inbox, and
  sender-aware delivery rules.
- Android voice and text, web and CLI access, device arbitration, and Ichoi
  interruption through a separately authorized adapter.
- Local development, independent test domains, backup and recovery, and
  operation without an available model.

Calendar integration means locally hosted compatible calendaring, like the
current Longhouse system, behind a replaceable adapter. A Google or Microsoft
account is not required. The user will build the new calendar service as a
separate project. Its native proposal and data extraction are not part of this
implementation. Do not move existing data as an incidental step. Bank access,
paid model accounts, and production credentials require their own configuration
and grants.

Keep the previously deferred research separate: automatic personality learning,
mood interpretation, reflection schedules, model training, and arbitrary workflow
loops. Preserve the records and extension points needed for those functions.
Temporary task identities and historical secret values remain excluded.

## Implementation defaults

These are proposed implementation defaults. They do not close the open prototype
design gates or change accepted authority boundaries. Foreign services need not
use the same software. Record a revision when a requirement or test changes a
default.

| Area | Initial implementation |
| --- | --- |
| Services | Go, generated CSIL APIs, service-specific stores and grants |
| Agent runtime | Magi-owned Python harness in this repository; MIT-era lo-agent lineage and lessons from OpenCode v2 and Pi; reviewed provenance before imports |
| Speech | Parakeet STT; compare replaceable TTS engines with at least eight voices; start on `192.0.2.20`, with separate services and additional model hosts permitted |
| Environment tools | mise with pinned recipes; evaluate open-source Microsandbox and Lima backends without making Kubernetes mandatory |
| Browser | Existing TypeScript clients and editor; add authenticated interaction and history views |
| Android | Kotlin native client; generated CSIL codec and LinkKeys SDK integration |
| Policy | Typed checks plus embedded CEL for operator expressions; deny on missing facts, evaluation errors, or exhausted limits |
| Events | Existing PostgreSQL durable pull service and local transaction helpers; no required NATS deployment |
| Metadata | PostgreSQL per service authority; sharing a physical database server is optional |
| Payloads | Immutable content store with local filesystem and object-store adapters; private content is not a public digest URL |
| Retrieval | Permission-filtered keyword and embedding candidates with explicit source links; exact sources remain authoritative |
| Workflow language | Native CSIL profile first; explicit Open Workflow conversion with loss reporting |
| Mobile notification | Direct FCM adapter plus authenticated inbox fetch; active clients also use a resumable stream |
| Test identities | Existing Example environment LinkKeys integration and a separate Partner environment integration; no new production identity domain is required |

The [policy experiment](identity-policy.md) supports the initial CEL choice.
Each service owns permit and deny composition. A successful expression is not
permission to ignore another source restriction. Preserve decimals, timestamps,
and identities through typed bindings. Do not coerce exact values to floats.

Use the existing [capture prototype](capture-experiment.md) as the basis for
reference substitution. The [retrieval comparison](retrieval-comparison.md)
supports combined candidate search, not autonomous evidence assessment by the
small test model. Keep model quality and protocol correctness as separate gates.

## Component contracts

Keep separate component CSIL entry points and an aggregate client. Each enabled
operation must have authentication, authorization, size limits, retry behavior,
and tests. Discovery reports implemented operations and schema digests. A type
in the aggregate schema does not mean that its operation is enabled.

| Component | Records and minimum operations | Authority boundary |
| --- | --- | --- |
| Registry | Register and revise capability descriptions; renew observations; resolve candidates | Advertisement grants no use of the resource |
| Identity and personas | Enroll stable applications; revise personas; assign subsystem responsibility; record exact allowance approval and withdrawal | LinkKeys authenticates; receiving services authorize |
| Policy | Read and revise scoped policy; evaluate receipt, processing, representation, and operations | Each control area has its own authorized editors |
| Sessions and interaction | Open a task session; submit an interaction; admit destination inputs; report presentation; correct an interpretation; read pending prompts | A conversation and a shared persona do not combine access rights |
| Model adapters | Describe capabilities; invoke or stream an admitted request; cancel where supported; retain usage and outcome | The task cannot select an unapproved endpoint or reveal data to an untrusted processor |
| Workflow | Draft, validate, publish, start, observe, respond, cancel, and reconcile | Definitions do not grant resource permissions; Corndogs owns task state |
| Execution | Register hosts; allocate environments; start and inspect commands; attach; cancel; hold; release; export | A host can refuse admission and owns local resource binding |
| Secrets | Resolve an admitted reference; issue or renew supported credentials; remove task copies | Values are not returned through history or generic discovery |
| Evidence | Admit a capture producer; append batches; acknowledge uploads; read history and exact context; record gaps | Evidence access is separate from credential resolution and source processing |
| Knowledge | Author revisions; query; explain; accept in a context; invalidate; delete; read origin changes | Magi arbitrates queries; sources retain resource authority |
| Events | Publish, subscribe, pull, acknowledge, inspect, and reconcile a restore | Producer and consumer transactions stay in their own stores |
| Inbox | Register endpoints; set delivery rules; schedule; fetch; report endpoint receipts | Sender identity and recipient policy determine delivery |

Logical components can share a process. Independent components do not read or
write one another's private tables. A shared in-process adapter must obey the
same admission and result contract as the remote adapter. No external caller
needs an invented persona or session merely to use events, workflows, or knowledge.

All native mutations use a stable caller request key, exact admitted input, and
an operation receipt. Repeated identical input returns the receipt. Changed
input at the same key conflicts. A receipt records acceptance, not an assumed
external effect. Authenticate every retry under current policy.

Use schema negotiation rather than silently changing the existing Q&A profile.
Keep its current commands working. Introduce new component methods and versioned
workflow definitions. Required unknown meanings fail closed. Unknown optional
assessment data can be retained without becoming a policy input.

## Connected request path

The conversation graph is primary. The path below describes one operation,
not a linear or mode-typed conversation. Graph segments can overlap. Each model
request uses an admitted projection; knowledge derives from graph records and
other evidence sources. Later interpretation cannot rewrite earlier execution.

```text
Authenticated interaction
  -> retained source interaction
  -> interpretation and destination selection
  -> separately admitted session inputs
  -> context and processor admission
  -> model response or proposed tool operation
  -> independent evaluation and resource admission
  -> workflow and environment execution
  -> retained result and evidence
  -> presentation or scheduled inbox delivery
```

Record each boundary before acknowledging its durable obligation. Use local
outboxes between services. Do not hold a database transaction across a model,
host, or foreign service call. A downstream outage leaves visible pending work,
not a successful result or an automatic new task.

The runtime first checks explicit reply and session references. It can propose
associations from pending prompts and permitted context when references are
absent. Validate those proposals outside the model. Ask for clarification when
the uncertainty matters to an action. Do not use one confidence threshold for
all tasks.

A new topic opens a new task session. Returning to an earlier task reuses that
session when authorized. The session stores input sequence and context revision;
each model call pins the exact admitted input set. Concurrent agents can use
different snapshots. A later input does not rewrite an earlier call.

For one reply to two sessions, retain one source interaction, its interpretation,
and two destination inputs. Admit each destination separately. Record partial
delivery explicitly and retry only missing receipts. Never copy a mixed private
reply wholesale into a less restricted destination. A correction adds records;
it does not redeliver prior input or undo an external effect.

The model proposes work. It does not write policy, declare its own admission,
mark a human step complete, or promote conversation into authored knowledge.
An agent stops for a result, a human request, a denied operation, a budget limit,
or a retained uncertain outcome. These are durable states, not lost Python state.

## Privacy and processing admission

Represent restrictions as a set of source and control-area requirements, not
one ordered public, group, or private label. Access must satisfy every relevant
requirement. This includes two private owners with different permitted readers.

The session family holds a current restriction revision. Child work shares that
boundary. Before a new source enters context, record its restrictions and update
the family revision. Context reads, queued outputs, model dispatch, tool dispatch,
and ordinary history reads recheck current restrictions. Cached authorization
does not override a newer revision.

Do not claim that this can recall information already sent. Admission and dispatch
need a defined ordering point against local policy changes. Record that point and
the revisions used. For a remote resource, the receiver makes its own current
decision; there is no cross-domain atomic policy transaction.

Check receipt and processing independently. Private data can go to an explicitly
trusted remote model. Provider failure does not authorize a different processor.
Source purpose also matters: ordinary journal access is not enrichment permission.
Bound context by purpose, relevance, token budget, and disclosure policy.

Administrator evaluation uses the administrator's evidence authority, not a
fabricated represented user. Processing restrictions still apply. Broader
learnings need separate assessment and publication records. Uncertain learnings
retain source restrictions, and public results cannot expose private source links.

## Identity and secret lifecycle

Keep stable persona and host identities separate from internal agent, task,
session, and attempt IDs. An executor admission record binds internal attribution
to the authenticated actor. A caller-supplied agent ID is not sufficient evidence.

Enroll application keys through supported LinkKeys flows. Keep bootstrap and
service credentials outside workers. Renew attestations before expiry; rotate
keys separately. The existing test profile has finite key and attestation
lifetimes. Neither a browser session nor an application attestation proves a
human approved a particular allowance.

Retain the exact allowance and authenticated approval evidence. Each receiver
chooses whether it trusts that approval authority. Check allowance withdrawal
separately from key status. Use fresh evidence for a new protected effect; refuse
the effect when its required freshness cannot be met. Cached signed identity
records must be revalidated, with both local and upstream ages checked.

The initial protected profile retains the current five-second upper bound for
key-status evidence. Allowance withdrawal evidence for a new effect uses the same
maximum age. Check known local withdrawal immediately. A service can require a
tighter bound. A read-only offline profile must be separately enabled; it cannot
authorize new effects. This bound permits a short observation delay, not instant
global revocation. Record freshness with the decision and test the delay.

An admitted tool can receive an SSH key, JWT, or another credential directly.
Resolve only its declared references. Register every value and binding before
exposure to a process or model. Store the reference form in snapshots, logs,
spools, and results. Keep literal `${secret:path:key}` text distinct from typed
secret segments. A history read never resolves a value.

Do not retain old secret values. A new attempt resolves current credentials and
records new bindings. A shell attachment needs authority to see credentials
already present in that environment. A lifetime hold does not renew credentials.
Removal of one task's copy must not revoke a shared source credential.

Credential scope is an enforced property of the credential and environment.
Independent model review can add a policy check; it cannot narrow an SSH key's
actual authority. Refuse unsupported required controls. Record trusted behavior
explicitly when a tool has wider authority than the task.

## Execution and human control

Use a host agent with pluggable native, container, and VM backends. It advertises
supported combinations of platform, isolation, resource limits, and named
bindings. Exact-host requests cannot silently move elsewhere. Host paths are
resolved from admitted named bindings, not arbitrary client strings.

Start with the already reviewed Reactorcide lifecycle mechanisms and secret
notation. Keep Magi's admission, command receipts, holds, and evidence transport
independent of Reactorcide's CI coordinator. Review each reused unit before
copying or extracting it. Do not import a log spool that discards old batches.

An environment records its admitted definition, host, control generation, active
uses, holds, export obligations, and cleanup phase. A command records its input,
generation, operation key, capture streams, launch observation, and outcome.
Reject stale generations. Persist an admission before launch.

The operating system launch and database commit are not one transaction. If a
crash leaves launch uncertain, inspect the executor's durable command record and
owned process or container identity. Do not launch a second command solely because
a lease expired. Reconcile external effects separately from process exit.

Joining is live. Pause controls new agent work. Command cancellation targets the
selected command. Run cancellation stops new admission and requests cancellation
of active work. None of these actions silently releases a human hold. Disconnect
also does not release a hold. Capture attached shell input and output under the
same secret and access rules as other commands.

Cleanup checks active uses, investigations, holds, and required export receipts
atomically against new attachment. Once cleanup starts, reject new attachment.
Delete only owned temporary resources. A writable host binding is never cleanup
material. Forced termination is a distinct admitted operation with retained gaps.

First unattended lab defaults: 30 minutes per task, 32 model calls, 64 tool calls,
two CPUs, 2 GiB execution memory, and 256 MiB evidence spool per environment.
Model hosting is a separate allocation. Paid model spend defaults to zero until
configured. Network destinations and host bindings default to none. These are
configurable admission limits, not capacity measurements or production defaults.

## Evidence and knowledge

The archive admits a producer for specific streams and limits. Batches have
sequence, sanitized bytes, binding metadata, and a final or gap marker. Commit
immutable payloads before publishing metadata that refers to them. An identical
batch gets the same receipt; altered content at that sequence conflicts.

Separate permission to finish an evidence upload from permission to execute.
On upload failure, retain the private sanitized spool. On capacity pressure,
stop new work before required capture becomes impossible. Record unavoidable
gaps. Binary artifacts need an admitted capture policy; opaque bytes must not
silently bypass known-secret protection. Keep byte digests of the stored form,
not digests of secret-bearing input.

Task history joins references across services under current access policy. It
shows original interactions, routing versions, actual session inputs, model
contexts, tool results, human changes, and later assessments. Relationship access
is checked too. Missing or deleted sources remain explicit reconstruction limits.
Playback has no effects and resolves no secrets.

Knowledge stores author-controlled revisions beside retained evidence. Markdown
documents are one representation, not the universal storage model. Keep source
authorship, acceptance in an adopting context, and access policy separate. Personal
claims need acceptance in the context that uses them. Deterministic system records
can be admitted directly. Conflict does not require one global winner.

Query admission selects permitted sources and processors before retrieval. Keep
scoped indexes and check access again before disclosure. Record source revisions,
observation times, precision, missing coverage, and calculations. Unknown support
does not become a confident answer because a small model supplied a citation.
Use exact calculation tools for metrics. Exploratory financial questions can
return a supported range and defer unavailable data collection.

Authorized deletion first removes current query access and invalidates derived
indexes. Publish origin change records for admitted mirrors. Track payload cleanup
and propagation separately. Keep only the tombstone metadata that policy permits.
Independent peers can acknowledge deletion; Magi cannot promise erasure of an
uncooperative recipient's copy. Restore must reapply deletions before serving
queries. Deletion can make older evidence incomplete.

## Workflow and event semantics

Extend the native workflow profile with call, compute, human, timer or event wait,
branch, fork, join, finish, and fail. Keep cycles unsupported in this release.
Compute calls the execution service rather than adding a second command engine.
Each branch has distinct outputs. A join explicitly selects all, any-success,
or a declared subset and states the treatment of unfinished branches.

Native expressions use the bounded typed policy expression interface. A workflow
expression is pure and receives explicit time values. Pin the conversion target
to Open Workflow v1.0.3, commit
`9b5b1da29e9d4fff2358580241e11aab22704a16`. Its expression configuration defaults
to jq. Do not run an imported jq expression as CEL or translate its spelling
without a semantic test. The initial converter accepts only constructs and
expressions for which it has a tested mapping. Preserve other input for editing
without enabling execution. This is a subset converter, not a conformant runtime.
Preserve layout separately from meaning.
[Pinned specification](https://github.com/open-workflow-specification/specification/blob/9b5b1da29e9d4fff2358580241e11aab22704a16/dsl-reference.md)

Use durable deadlines for waits. Distinguish an intentional human wait from a
missing automated worker. The registry admits workers for capabilities and scope;
worker registration alone grants no task. Remote workers use component APIs,
not workflow database credentials. Retire direct database access from the first
Q&A worker only after the new execution path passes its compatibility tests.

The event service retains delivery obligations. The sender's outbox joins its
own state transaction. The receiver's inbox joins its own effect transaction
where possible. For an external effect, record intent and its provider receipt.
A lost response produces an uncertain outcome unless the provider supports a
safe status check or idempotent retry. Do not promise general exactly-once effects.

Federation retains original signed bodies and authenticates each delivery attempt.
Check current receiver policy at admission. Endpoint discovery is not trust.
Mirrors receive origin changes; they submit edits to the origin. Test both domain
directions and independent versions, not aliases of one installed authority.

## Inbox and Android interaction

The inbox owns delivery choices, not the event service. Evaluate verified sender,
event kind, resource, recipient rules, permitted urgency, schedule, and endpoint
availability. Recheck due items after policy or calendar changes. Store IANA
timezone and the rule revision. Test daylight-saving changes and holidays with
an injected clock. An untrusted emergency flag cannot bypass quiet hours.

Use distinct receipts for provider acceptance, device receipt, presentation, seen,
and explicit acknowledgement. FCM carries an opaque inbox reference by default.
A confidential preview needs its own policy. FCM priority is a transport setting,
not Magi's emergency authority. High-priority delivery is for time-sensitive
visible notifications and can be downgraded. It is not a hidden continuous work
channel. [FCM priority](https://firebase.google.com/docs/cloud-messaging/android-message-priority)

Provide two Android modes. Regular mode supports text, push-to-talk, and listening
explicitly started while allowed. Assistant mode offers the platform assistant
role without requiring it for ordinary use. Never request the role implicitly.
Expose capture availability and interruption rather than claiming continuous
coverage. Android limits background microphone startup.
[Background service restrictions](https://developer.android.com/develop/background-work/services/fgs/restrictions-bg-start)

Use one microphone capture path within the Magi application for wake detection
and STT. Sharing audio with another application depends on Android policy; two
ordinary apps cannot assume simultaneous capture. Record when capture is silenced
or the input device changes. Do not use accessibility privileges as a microphone
workaround. [Audio input sharing](https://developer.android.com/media/platform/sharing-audio-input)

Keep wake detection, STT, speaker assessment, TTS, and presentation separate.
The user selected Parakeet and a replaceable TTS contract with at least eight
voices. The [prototype design](demo-prototype.md) compares alternatives to Breeze.
CUDA is an engine placement requirement, not an Magi-wide restriction. Separate
speech services can use shared or different hosts. Personal TTS cloning is excluded.

Speaker estimates support conversation attribution and cross-talk handling.
They do not replace authentication. Policy can require a LinkKeys authentication
request for sensitive operations. Simple requests need no blanket confirmation.
The receiving service retains authorization and exact-operation checks.

Support overlapping discussion, planning, instruction, and explanation in the
conversation graph. Modes are optional segment analyses, not conversation types
or permission grants. Preserve the accepted connected demonstration.

Run local multi-engine transcription after live capture. Keep alternatives and
disagreements, then expire raw audio under policy. Later corrections do not rerun
effects. If speech fails, use an admitted alternative or send text with observed
failure evidence and request human help when needed.

Arbitrate duplicate microphone observations without combining two people's work.
Use one bounded voice presentation lease for the selected user and channel.
Text can continue independently. Output can move to a different device after
readiness and audience checks. Pause Ichoi only through an admitted interruption;
resume only when that interruption still owns the unchanged playback state.

No iOS implementation is required for this first client release. Native Android
testing must include a physical device, microphone contention, screen lock,
network loss, permission withdrawal, push receipt, and both application modes.
An emulator or foreground browser test cannot close those gates.

## Local calendar integration

Use the current local calendar service through an adapter. Preserve its resource
identity, supported operations, timezone meaning, and revisions. Provide typed
Magi capabilities for its actual features. Report unsupported functions instead
of inventing reservations, conflict guarantees, or recurrence semantics that the
underlying service does not provide.

Use a read-only live source adapter and synthetic calendars for the connected
release tests. Add writes only through explicit resource grants and exact-operation
admission. The local service remains the calendar record authority. A later adapter
can target the user's new service without changing persona, session, workflow,
or evidence ownership. The native calendar design and Longhouse migration remain
separate projects. Do not reinterpret an old field to hide missing meaning.

## Deployment and recovery

Keep the current local workflow and public Q&A working while new components are
added. Use separate stores, roles, and named services. The existing router owns
TLS termination. Restrict changes to Magi resources in the authorized test system.
Do not change shared LinkKeys rate limits to hide an inefficient client.

Use authenticated resumable change feeds and bounded identity resolution instead
of frequent authenticated polling. Enforce freshness on cached records, not just
cache age. Retain a polling fallback with explicit load limits.

Migrations use expand, migrate, and contract steps with a compatibility window.
Back up before destructive changes. Drain affected roles before an incompatible
schema change. Record application, schema, policy, model, and image revisions.
Keep a manual recovery procedure that does not need a functioning model or Magi.

Automatic rollback must also work outside the candidate's failure boundary.
Check readiness and synthetic user operations before promotion. Use bounded
thresholds, a known-good artifact, and worker fencing. Keep database changes
compatible during rollback; changing a binary does not undo data or external
effects. Preserve a manual recovery path if automatic recovery fails.

For the unattended lab, start with a five-minute backup recovery-point target
and a thirty-minute recovery-time target. These are acceptance targets, not
existing guarantees. Ordinary process loss must not lose an acknowledged local
commit. Restore creates a generation fence and reconciles cross-store receipts
before new external effects. Replica count is not database or artifact HA.

Health checks expose store access, withdrawal freshness, sweep age, queue age,
capture backlog, and capacity without private task content in metric labels.
Active requests report a blocking outage even during quiet hours. Preserve
operational evidence independently of the conversation archive.

## Implementation sequence and completion

This is the broader release plan. First close the
[prototype design gates](demo-prototype.md) and build its connected demo.
Do not postpone speech and non-Q&A interaction until all wider release functions are
complete. The rows below remain internal verification points, not requests for
the user to say "continue" after each component.

| Order | Connected work | Required proof |
| --- | --- | --- |
| 1 | Personas, scoped policy, sessions, interaction admission, and retained context | One persona serves two users without context leakage; one task continues and a new topic separates |
| 2 | Capture, secret bindings, model registry, Python agent loop, and independent evaluation | Real local model; current credentials; no values in durable evidence; denied tool does not run |
| 3 | Host registry, execution APIs, native and container backends, live attachment, and holds | Exact host; named directory; stale generation rejection; crash reconciliation; join does not pause |
| 4 | Workflow branches, waits, joins, cancellation, remote workers, and authenticated editor | Human response and parallel branches survive restart; uncertain effects do not repeat |
| 5 | Knowledge APIs, source adapters, Markdown revisions, scoped search, and history | Acceptance isolation; live-source purpose; recall; original and corrected interpretation remain distinct |
| 6 | Events, inbox, schedules, FCM, browser interaction, and Android client | Sender-aware quiet hours; one reply advances two sessions; lost connection recovers without duplicate work |
| 7 | Windows and macOS backends, speech, and device arbitration | Tests on the target platforms; credential isolation; output selection; interruption ownership |
| 8 | Independent-domain installation, operational controls, backups, and restore | Both domain directions; explicit refusals; actual restore; original local workflow still passes |

Run the existing [T01 through T71 requirements](test-environments.md) through
the real connected boundaries. Keep component and end-to-end results separate.
Repeat crash and concurrency checks. Test each third-party client through its
standalone component API. Use synthetic private records and credentials before
admitting real private data.

Every release scenario must identify the source revision, configuration, actual
services, passed assertions, and missing coverage. An unavailable device or
provider is an open gate, not a skipped test counted as release success.
Small-model correctness checks do not prove general planning or answer quality.

The release is complete only when these connected paths and operational gates
pass. Future research and the separate calendar project do not block it.

## Connected acceptance scenarios

Use these scenarios to verify the implementation sequence. Preserve the wider
T01 through T71 matrix; these examples do not replace its negative cases.

1. Ask a question, ask a related follow-up, switch tasks, then return to the
   earlier task. Inspect each exact model context. The follow-up has its admitted
   context; the unrelated task does not inherit it. Repeat with two users who
   share one persona and with a restarted coordinator.
2. Ask the Engineering Manager to investigate a synthetic service failure on a
   named host. It retrieves permitted project notes, evaluates a proposed tool,
   and runs it with a synthetic credential in an admitted environment. Join live,
   make an explicit context update, pause the agent separately, and release the
   human hold after completion. The archive contains references, not the value.
3. Start a workflow from the other domain. The receiver accepts one operation and
   denies another under its own policy. A human step waits without a worker.
   Complete it once, run parallel branches, then deliver a notification after
   quiet hours. Repeat every receipt and lose a reply after an external effect.
   Verify that no uncertain effect is repeated.
4. Ask about synthetic adult-child support with missing purchases and a duplicate
   bank and Venmo entry. Return a range with source coverage. Run a separate
   campaign-metrics query with reproducible exact calculations. Accept a personal
   interpretation in one user's context without changing the other user's view.
5. Answer two pending prompts in one message. Advance both authorized sessions,
   retain the shared source, then correct one association. Historical context
   shows what each worker actually received. Reprocessing causes no tool effect.
6. Use Android voice while a text conversation continues elsewhere. Type a reply
   to the spoken prompt, then send an unrelated text reply. Test microphone
   contention, two devices hearing one wake, and the phone-to-Ichoi fallback.
   A device that cannot capture reports unavailable capture rather than silence
   presented as a successful observation.
7. Recall a topic inside an interleaved exchange three weeks earlier. Repeat with
   the archive unavailable during quiet hours. Return known failure evidence,
   not a fabricated memory or explanation for a missing alert. Measure recall
   time separately from the acknowledgement.
8. Restore the independent stores and artifacts to a recorded recovery point.
   Reapply withdrawal and deletion state, reconcile uncertain commands, and
   resume pending deliveries. Verify holds, current access, and source revisions
   before new execution. Keep unrecoverable gaps visible.

## External setup

Implementation can proceed without production access. Live acceptance will need
the following specific inputs when their stages are ready:

- A user-controlled Android device, microphone consent, and a test FCM project.
- Approved Windows and macOS VM hosts and their named test resources.
- Partner environment DNS, TLS, and a separate LinkKeys authority, with authority to manage
  those resources. Example environment access does not imply access to that infrastructure.
- Model endpoints, spending limits, and source grants for any test beyond the
  existing local model and synthetic records.

Do not purchase services, publish a mobile application, install a device-owner
profile, change an existing default assistant, or migrate production data as
an implicit implementation step.

## Next Actions

Resolve the prototype's TTS comparison and detailed graph and authentication
contracts. Record the internal harness's source provenance. Then close
the component contracts and implement the connected demo. Keep the deployment
usable. Escalate requirement conflicts, missing authority, and choices that
change a user's data or device behavior. Routine checkpoints need no new handoff.
