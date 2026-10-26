# Implementation plan

Status: local experiments and service prototypes. Production runtime is incomplete.

Update 2026-09-29: Corndogs 0.8.0 passes W01's service-level recovery matrix.
Two protected runs passed 146 checks across all five configurations.
See the [results](corndogs-recovery-080.md) and selected
[adapter contract](corndogs-adapter.md). W01's service investigation is complete.
End-to-end T08/T09 remain open until W05 tests the adapter journal and effects.

Update 2026-09-30: W02's [local experiment](identity-policy.md) passed 495
checks across three runs. The user selected authenticated human approval of
exact allowance bytes, recorded by Magi. Embedded CEL is recommended for the
first Go prototype. Real login, resolver, durable approval, and cross-domain
approval evidence tests were still open at this date. The update below records
their completion in disposable systems.

Update 2026-10-03: the user selected Magi-owned execution with selected
Reactorcide components, native and sandboxed host work, human control, and a
reference-only evidence archive. These are design changes, not new test results.

Update 2026-10-05: the interview establishes stable persona identities shared
by workers, task sessions, scoped trust, direct credential injection, and
evaluation outside worker contexts. Temporary identities and historical secret
values are outside scope. These changes do not add passing runtime tests.

Update 2026-10-06: knowledge can span different sources and stores under Magi.
Interaction can span concurrent voice and text, with one reply advancing
several sessions. Preserve linked evidence layers for later interpretation.
Assessment details, reflection, adaptive learning, and deletion mechanics
remain open. T53 through T71 are requirements, not test results.

Update 2026-10-09: the user approved the knowledge requirements. The
[record and storage proposal](knowledge-storage.md) defines minimum records,
API behavior, a shared-reply example, storage options, and proposed measurement
thresholds. Package completion gates remain open.

Update 2026-10-11: the user approved that proposal as a starting point. The
[synthetic experiment](knowledge-experiment.md) passes 81 leaf tests across
three runs, including Go/Rust codecs and PostgreSQL recovery. Full-text search
finds 50 of 100 expected sources per pass and misses all paraphrases.
This is partial W03/W10/W13 evidence, not completion of those packages.
Authentication, public APIs, capture, and independent-domain integration remain open.

Update 2026-10-12: a [local retrieval comparison](retrieval-comparison.md) tests
six methods with CPU inference. Combined search with explicit evidence links
recovers all required records for 18 supported test questions in two runs.
Methods still return candidates for missing-evidence questions. This is partial
W10 retrieval evidence, not answer validation, scale acceptance, or W02 completion.

Further work on 2026-10-12 adds [support checks](evidence-support.md) and
[reference-only capture](capture-experiment.md). The small model matches the
expected support mode for only 8 of 20 test questions. Do not select it as
the default evaluator. Typed substitution, Go/Rust capture codecs, current
credential preparation, and private spool process recovery pass local tests.
The full regression has 237 leaf passes across three runs. W02 and authenticated
capture integration remain open; no package completion gate changes.

Update 2026-10-13: W02's [service experiment](identity-service.md) passes 102
leaf checks across three runs. Two independent LinkKeys 0.16.2 servers test
real login and key resolution. Durable exact-byte approval, CSIL APIs, receiver
policy, withdrawal, key expiry, key revocation, and process recovery pass.
W02's disposable integration gate is complete. Production deployment, database
HA, and capture and knowledge integration remain open.

W03 update, 2026-10-13: the [native protocol](native-protocol.md) now has
compiled CSIL, generated Go and TypeScript clients, fixed wire fixtures,
strict decoding, and exact-byte signature checks. Three runs pass the local
protocol and HTTP tests. A read-only discovery service runs in the primary
test domain. This does not complete W04 or the two-domain W07 scenario.

W04 update, 2026-10-14: the [standalone event service](event-service.md)
has a separate executable, CSIL schema, PostgreSQL store, and LinkKeys boundary.
Producer outboxes and consumer inboxes stay in each application's own store.
Recovery tests cover duplicates, lost acknowledgements, process termination,
stale publication claims, and database restart. Two disposable LinkKeys domains
exercise the API in both directions with separate event stores and SQLite
consumers. The initial transport is durable pull. Public deployment, backup
reconciliation, quotas, and workflow integration remain open.

Further work on 2026-10-14 adds database-backed event quotas, bounded operator
inspection, and a storage-generation restore fence. Real backup/restore tests
compare retained and missing receipts. These controls do not reconstruct lost
cross-store effects automatically.

W05 now has a [Corndogs operation journal](workflow-journal.md), exact-result
requirements, and internal human-task bindings. Tests cover file storage and
two Corndogs servers over PostgreSQL, process termination, lost replies,
same-state reassignment, and result/outbox recovery.

The [independent workflow service](workflow-service.md) now adds immutable
definitions, draft edit versions, typed connections, and prompt templates.
Its initial interpreter supports call, direct human response, and finish steps.
A local graph editor uses the same records and validator. A separate coordinator
now recovers admitted transitions with durable scheduling and expiring leases.
It does not dispatch tools. A separate timeout evaluator now checks admitted
call queues. Its readiness gates new tool calls. The worker registry and remote
editor login remain open.
The [question and answer setup](question-answer.md) now connects a signed CLI,
the workflow service, a Python model adapter, and retained call evidence.
A pinned small CPU model supplies the real-inference CI check. Operator-installed
workers select an exact definition, scope, and requesting application. This is
not remote worker registration or a complete agent harness. Private source
policy, credentials, knowledge retrieval, and public deployment remain open.
Longhouse is optional. Its adapter needs the
[assignment contract](longhouse-workflow-contract.md), not ownership of workflows.

## Connected delivery

The [TTS candidate test](tts-candidate-test.md) now provides a local listening
set and pinned CPU adapters. Kokoro is the first CPU deployment candidate.
The user kept twelve Kokoro and seven Qwen voices from the introduction samples.
Qwen CustomVoice remains optional for another admitted host or runtime.
The [speech service](speech-service.md) now has signed admission, durable request
records, native model workers, and a local test-key setup. The
[conversation demo](conversation-demo.md) connects graph records, model context,
text, and voice. It preserves one input shared by two sessions. Persona mappings,
two listening flags, private processing, and public deployment remain open.
The [offline development exercise](offline-development.md) adds an Magi-owned
Python proposal loop, an offline container profile, durable tool receipts,
live inspection, and review of an exact candidate. It cannot apply source or
deploy a release. The private operator policy selects the human release
approver for Example environment. Authenticate that account and the exact approval;
do not infer human approval from a task application's key.

The [demo prototype design](demo-prototype.md) records remaining design gates
and the first useful connected demonstration. The
[integrated release design](integrated-release.md) retains the wider release
scope. Speech and graph-based non-Q&A interaction belong in the demo, not only in a late client
phase. Continue between
work packages without asking the user to approve each routine next step. Keep
progress and missing coverage visible. Stop for missing authority or a material
requirement conflict, not merely because a package has passing tests.

The protected Example environment Q&A path now passes real LinkKeys authentication and
retained-evidence checks. It still has no conversation context or general tools.
See the [deployment guide](question-answer-example.md). Android is the first
mobile client, with both regular and optional default-assistant modes.

## Work packages

| Package | Depends on | Deliverable and completion criterion |
| --- | --- | --- |
| W00: baseline | None | Reviewable decisions, contracts, evidence, test topology, and this plan. Documentation exists; runtime is absent. |
| W01: recovery experiment | W00 | Service boundary verified on 0.8.0; adapter contract selected. Complete T08/T09 end-to-end evidence in W05. |
| W02: identity and policy experiment | W00 | Complete as a disposable experiment. Policy fixtures and real authentication, durable approval, and cross-domain evidence checks pass. See the service report for deployment limits. |
| W03: native protocol | W01, W02 | Initial baseline complete. Compiled identities, sessions, trust, commands, events, receipts, task links, environment control, secret bindings, and evidence links pass Go/TypeScript fixtures. Optional assessment extensions remain possible. See the native protocol report for limits. |
| W04: durable event path | W03 | Initial standalone pull service and local transaction helpers are implemented and tested. T02 through T05 pass at this boundary. T06 has an explicit unknown-outcome path. T07 has old-event tests, not a measured two-hour outage. Complete external-effect and operational recovery gates before public deployment. |
| W05: minimal workflow | W03, W04 | Independent service, versioned call/human/finish definitions, direct responses, journal recovery, coordination, and scoped timeout evaluation are implemented. Add worker registration and source-policy processing. T10 has a direct-response path; Longhouse remains an optional integration. Complete T11. |
| W06: workflow language and editor | W05 | Typed draft APIs and a local graph editor exist. Add authenticated remote editing, Open Workflow mapping, branches, cancellation, and simulation. Pass T12 and T13; extend T20 beyond the initial subset. |
| W07: persistent two-domain lab | W04, W05, W06, W08, domain ready | Two separately installed systems, each with its own LinkKeys integration and authority; CI-tested artifacts; complete scenario in both directions. Include service versions and configurations for each system. |
| W08: personal inbox | W04, W05 | Sender-aware rules, durable schedules, endpoint bindings, receipts, and one push adapter. Pass T14 and T15 locally before persistent deployment. |
| W09: agent and execution | W02, W05, W13 | Magi coordinator and executor, persona-authenticated workers, task sessions, direct credentials, independent evaluation, budgets, host bindings, and human control. Pass T06, T21, T22, T25 through T28, T36, T39 through T45, T51, and T52. Test T65 through T67 with simulated channels; repeat W13 capture tests through real executors. |
| W10: knowledge authority | W02, W04, W13 | Magi query arbitration, source-specific records and indexes, Markdown authoring, scoped acceptance, live access, sharing, authorized deletion, and filtered retrieval. Pass T18, T19, T53 through T64, T70, and T71 at the query boundary. No single graph engine or full reflection system is required. |
| W11: voice and interaction | W08, W09, W10 | Shared capture, wake detection, STT/TTS adapters, cross-channel presentation, and Ichoi interruption. Pass T16 and T17; repeat T65 through T67, T70, and T71 on target devices with concurrent text. |
| W12: calendar integration | W02 | Integrate locally hosted compatible calendaring through a replaceable adapter. The user owns the separate native calendar project and Longhouse extraction. Do not make that migration an Magi release dependency. |
| W13: evidence and history | W02, W03, W04 | Reference-only capture, durable uploads, original interactions, interpretation and actual-input links, extensible assessments, task views, privacy, and current-input preparation. Pass T29 through T35, T37, T38, T46 through T50, T68, and T69 at the capture and query boundary. |

Package numbers identify work; dependency order determines execution order.
The first delivery target is W01 through W05 with automated tests. Public DNS
setup does not block those packages. Do not require a diagram editor, speech
model, or new calendar implementation to prove workflow recovery.
W05 can use a bounded test executor and a typed service call. It does not
require the full W09 runtime. The persistent workflow scenario first uses that
executor. Full foreign environment and agent-history tests require W09 and W13.
Knowledge and interaction prototypes can use simulated sources and channels.
Their complete integration gates require the real query and device paths.
Do not make advanced mode learning or mood and tone assessment prerequisites
for the first protocol, workflow, or capture implementation.

## W01 experiment details

Use real Corndogs with a disposable durable store. Record exact server, client,
and schema versions. Insert a proxy that can drop one response after the server
applies a request. Pause one worker until its task is reassigned.

Observe submission recovery, completion guards, timeout evaluation, and
queryable history. Do not infer atomic compare-and-set from a field named
`current_state`. Inspect implementation and test the operation.

Output: a short report, executable regression cases, and a selected adapter
contract. If a Corndogs extension is necessary, describe the required behavior
and compatibility path before editing that project.

## W02 policy comparison

Use the same fixtures for Cedar, CEL, and Cerbos candidates:

- Household guest may queue music on one player until a stated time.
- Assistant may propose appointments but cannot delete commitments.
- Trusted third party signs an attribute; an untrusted party signs the same value.
- Indefinite allowance remains valid until withdrawal under receiver policy.
- Two policies disagree, or an expression encounters missing/invalid data.
- Decimal, timestamp, identity, and resource values retain their exact meaning.

Measure startup, memory, evaluation latency, policy validation, and operational
complexity. Verify error handling and default denial. Select a service-local
integration; do not mandate one evaluator for foreign services.

## W03 protocol work

Create source schemas under `csil/` only when the tables are ready to compile.
Pin csilgen and transports. Generate clients and descriptors. Add bounds for
payload size, depth, collections, deadlines, and error messages.

Keep experimental subsets under their experiment directory until service
review. The knowledge and capture subsets have pinned Go/Rust output and tests.
Promote reviewed types to `csil/`; do not treat experiment service IDs as final.

Specify signature context and byte preservation. Test altered recipient,
schema, identity, and body. Keep verified signed bytes distinct from decoded
application values. Verify both signing and verification in each selected SDK.

Review persona revisions, task sessions, source restrictions, scoped trust,
evaluation records, environment bindings, control generations, and human holds.
Include SecretBinding, evidence streams, context snapshots, and query permissions.
Use typed secret segments alongside the existing display notation. Test literal
reference text, ambiguous values, chunk boundaries, and current resolution failures.

Represent query authority without requiring a persona or represented human.
Review source records, interaction evidence, interpretations, and actual inputs
with many-to-many links. Version optional assessment extensions. Do not freeze
a universal relationship hierarchy or require a numeric confidence score.

## W09 execution work

Use Python for agent execution. Keep the current Go services and browser client.
The user now requests an Magi-owned Python harness in this repository. Use the
MIT-era lo-agent lineage and lessons from OpenCode v2 and Pi. Record imported
file provenance and notices; do not import later restricted lo-agent changes.
Preserve Magi-owned graph context, admission, execution, and evidence. Local
logs must not retain raw secrets before later export.
See the [prototype review](demo-prototype.md) for source findings.

Evaluate pinned Microsandbox and Lima mechanisms with mise-based recipes.
Separate host placement, isolation, development-container configuration, and
tool installation. Do not infer native Windows or macOS guest support from a
microVM runtime's host-platform list.

Select the smallest Reactorcide components after a dependency review. Do not
make Reactorcide job submission the Magi execution contract. Start with one
host executor and one coordinator process. Keep claims, receipts, control
revisions, and human holds durable so replicas can share coordination later.

Prove exact-host selection without Kubernetes. Test native processes and a
sandbox with a named host directory. Declare missing platform controls and
reject requests that require them. Then add Windows and macOS VM mechanisms
with the same admission and evidence contracts. Do not claim platform support
until the relevant tests run on that platform.

Test live joining, separate pause and cancellation, plan changes, explicit release,
coordinator failover, and cleanup races. Keep environment lifetime separate
from Corndogs task state. Use stable persona identities for workers. Internal
task IDs are not new principals. Add stable identity bootstrap and credential
renewal only through supported protocols; require no temporary-identity feature.

Test direct injection of synthetic SSH keys and JWTs. Record actual credential
authority and enforced limits. Compose tool evaluation context from graph records
outside the proposing worker. Test task-specific failure investigation for
service and media tasks as well as code. Users supply context updates through
the initiating persona, not through automatic file-watcher messages.

Test several active task sessions through one interaction point. A reply can
reach multiple sessions, with admitted content and a receipt for each one.
Retries and routing corrections must not silently repeat effects. Voice
simulation tests coordination before real STT, TTS, and device behavior.

## W10 knowledge and W11 interaction work

Use [the interview scenarios](knowledge-and-context.md) and the
[record and storage proposal](knowledge-storage.md) for the first experiment.
Compare the proposed text baseline with optional vector retrieval. Support
distinct representations and scoped indexes; test
source links across stores and live calendar access. Keep Magi in control of
query interpretation while each source retains its resource authority.

Test contextual communication, human authorship, subject-scoped authority, and
acceptance in the adopting context. Source enrichment rules are separate from
read access. Implement authorization for operations when those operations are
introduced; do not expose an unprotected deletion API while policy is pending.
Detailed organizational policy and deletion behavior can be designed later.

Use financial comparison, campaign metrics, camera inspection, and protocol
explanation fixtures. Model missing sources and limited user effort. Do not
infer a new LinkKeys protocol implementation from a walkthrough fixture.

Test interleaved conversations, replies to earlier prompts, typed answers to
spoken questions, unrelated text work, and a shared reply for two sessions.
User settings and context select the output path. Test reintroduction of a
topic after an interrupted context switch. Keep sessions and access scopes separate.

Measure historical recall from a mixed interaction three weeks earlier. Define
the workload and exact thresholds before claiming the response targets. Repeat
with unavailable sources during quiet hours. Distinguish initial acknowledgement,
completed recall, and a failure response; do not report a holding message as recall.

## W13 evidence work

Preserve the value-to-reference binding at resolution or issuance. Substitute
before durable capture. Do not copy a value-only redaction set and assume that
it can recover the correct reference. Use synthetic fixtures for provenance,
ambiguity, stream boundaries, encodings, and policy refusal.

The capture experiment implements the local substitution and spool boundary.
Connect it to authenticated producer admission and remote upload next. Its
process-crash checks do not prove host recovery or complete executor capture.

Build retained streams, manifests, publication receipts, and context snapshots
before a history UI. Separate late upload permission from execution authority.
Query a session with multiple agents, child work, handoffs, tool results, and
human changes. Test separate task sessions within one conversation. Enforce
source restrictions at each record and graph edge, including derived views.

Playback has no effects and does not resolve secrets. Input preparation for a
new execution uses current authorization and records current credential bindings.
Do not retain historical values for replay. Test administrator evaluation under
its own authority, scoped processor trust, and conservative learning assessment.
Keep learning classification, publication, and training selection separate from
immutable capture and the original session's privacy.

Preserve more than a flattened transcript. Reassemble source evidence, prompt
presentation, interpretation revisions, and actual per-session inputs. Add a
later assessment with optional dimensions and context-specific weights while
leaving earlier records intact. Test gaps and authorized deletion limits.

Reprocessing uses current permissions and must not deliver old replies or rerun
commands. New evidence and human review can also add assessments. Reflection
schedules, adaptive modes, and mood or tone models are later work, not completion
criteria for the initial extensible capture records.

## Integration requests to prepare

These are requirements to validate. The Corndogs requirement is released and
tested; the other rows remain proposed integration work.

| Project | Requirement to validate |
| --- | --- |
| LinkKeys | Human allowance attribution, stable persona and host authentication, bootstrap and renewal, and supported statement signing/verification. No temporary task identity requirement. |
| Corndogs | 0.8.0 supplies the tested service contract. The Magi journal tests intent recovery and bounded local effects. Add full interpreter and cross-store restore tests. |
| Longhouse | Optional assignment adapter: external step binding, idempotent private task creation, and durable completion feed. It does not own workflow definitions or runs. |
| Reactorcide | Review selected lifecycle, secret notation, value matching, and telemetry components for reuse. Durable CI/CD events remain a separate optional integration. |
| Ichoi | Playback revision and interruption ownership for conditional resume |
| lab-speech-engine | Backend-neutral streaming recognition contract |
| csilgen | Descriptor and code generation for the selected contract; request only demonstrated gaps |

Before a change in another repository, read its instructions and shared notes.
Preserve existing changes. Keep the shared ecosystem notes current after the
change. Do not commit or push unless the user explicitly asks.

## Completion evidence

Each package records implemented behavior, test commands, exact dependency
versions, measured limits, and remaining gaps. A design document is not proof
of implementation. A local checkout is not proof of a deployed release.

For a deployment, record target domain, service identity, artifact digest,
database migration level, and recovery procedure. Keep credentials out of the
report. Manual acceptance is additional evidence, not a substitute for tests.

## Next Actions

First close the [prototype design gates](demo-prototype.md). Review the requested
speech candidates, harness provenance, sensitive-operation authentication,
conversation-graph records, and the
self-development acceptance path. The work below remains necessary but does
not replace that design review.

1. Improve the tested evidence sufficiency step with a new evaluation version.
   Use new questions for tuning and larger workloads; preserve the frozen results.
2. Add worker registration and uncertain-effect reconciliation.
   Test restore against the journal and effect authorities. Add storage alerts.
3. Connect the environment and evidence handlers to admission policy. Their
   W03 record shapes do not enforce lifecycle rules by themselves.
4. Connect the local graph editor to authenticated draft APIs. Extend the tested
   workflow subset only with explicit execution rules and recovery tests.
5. Extend the read-only example deployment with tested services. Prepare
   the independent partner.local system without changing shared deployments.
