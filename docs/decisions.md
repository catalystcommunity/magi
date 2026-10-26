# Design decisions

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

Baseline: 2026-09-27. Interview decisions incorporated through 2026-10-06.
This document records the user discussion.

On 2026-10-09, the user approved the [knowledge and context requirements](knowledge-and-context.md)
as the design baseline. Revise these requirements as experience with the system
identifies needed changes. Proposed mechanisms and open choices remain separate
from the approved requirements.

On 2026-10-11, the user approved the record and storage proposal as a starting
point. The [first experiment](knowledge-experiment.md) tests that direction.
This approval does not select a complete production storage stack.

On 2026-10-14, the user approved independent workflow ownership. Longhouse
remains an optional task and assignment integration. The workflow service owns
reusable definitions, typed connections, prompt templates, and run records.
Its graph editor is a client of that model, not the execution authority.

`Accepted` means the user established the boundary. `Proposed` means this
design recommends an implementation. `Open` means evidence or a choice is
still necessary. None of these statuses means that code is implemented.

## Accepted boundaries

| ID | Decision | Reason |
| --- | --- | --- |
| A01 | Keep LinkKeys authentication separate from resource authorization. | A resource service owns the meaning of access to its data. |
| A02 | Use signed allowances to express delegation intent and attributes. | A receiving service can interpret a statement under its own policy. |
| A03 | Allow bounded and indefinite allowance duration. | A user can authorize an ongoing relationship. |
| A04 | Use Corndogs task states and workers for workflow execution. Exclude Temporal. | Reuse the existing task service without a heavy workflow runtime. |
| A05 | Represent human steps explicitly. Accept direct authorized responses or use an optional assignment adapter such as Longhouse. | A human wait does not require a worker or task manager. A separate human hold can retain an environment. |
| A06 | Keep Magi runtime ownership and replaceable inference and harness adapters. | Avoid permanent dependence on a commercial harness. A65 selects the requested initial harness; its current license needs review. |
| A07 | Use CSIL for native APIs and data models where possible. | Preserve types and support independent clients. |
| A08 | Preserve cross-domain operation and local resource authority. | Households and organizations must cooperate without shared administration. |
| A09 | Keep an authoritative origin for each knowledge record. Submit edits there. | Mirrors must not create conflicting authoritative versions. This does not require one graph or store. |
| A10 | Provide at-least-once delivery for accepted durable obligations. | A disconnect must not silently discard work. |
| A11 | Identify senders and recipients for protected delivery. | Sender identity is part of routing and trust decisions. |
| A12 | Exclude Home Assistant from the baseline. | Build the required voice and device functions directly. |
| A13 | Separate wake-word detection, STT, TTS, and conversation handling. | Each function has different placement and resource needs. |
| A14 | Start with Parakeet STT. Keep replaceable adapters for other speech models. | Confirmed for the demo on 2026-10-15. Model revision, runtime, and hardware performance still need tests. |
| A15 | Build Magi execution from selected Reactorcide components, not its job coordinator. Keep Windows and macOS VM support. | Revised by the user on 2026-10-03. Magi needs constrained orchestration, not a CI service dependency. |
| A16 | Own notification identity and routing. Avoid ntfy as the baseline. | Provider topic names must not define Magi identity. |
| A17 | Design a future CSIL calendar model with current-protocol adapters. | Interoperability must not constrain the native model. |
| A18 | Use catalystsquad.local as primary and partner.local as secondary. Install independent systems, each with its own LinkKeys integration. | Preserve existing integrations and test cooperation between separate domain authorities. |
| A19 | Prioritize automated unit and integration tests. Add persistent domain tests. | Persistent tests confirm real integration after repeatable tests pass. |
| A20 | Inspect jr2 for useful lessons only. Do not reuse its code or let its design define Magi's direction. | External observations can reduce development effort without changing the user's requirements. |
| A21 | Record authenticated human approval of the exact allowance bytes in Magi. | Selected by the user on 2026-09-30. An application signature does not replace human approval. |
| A22 | Compose target host, execution mechanism, resource bindings, authority, and limits independently. Support native execution and host directory mounts. | A task can need specific hardware outside Kubernetes. |
| A23 | Make the Magi coordinator horizontally scalable. It manages persona identities and task secret access. | Workers use persona identities. Resource policy, not a persona name, grants authority. |
| A24 | Permit human inspection, shell access, plan changes, and authorized concurrent work. Retain a shared environment until its human hold is released. | Task completion or client disconnection does not imply human release. |
| A25 | Capture model interactions, logs, results, and other available evidence before assessing its later value. | Training selection and knowledge admission must not discard evidence at collection time. |
| A26 | Use Reactorcide secret reference syntax and adapt its value-matching pattern to archive references instead of secret values. | Authorized replay can resolve references without placing credentials in the evidence archive. |
| A27 | Permit short-lived credentials in context for the authorized session when needed. Archive only their references. | Some tools and model sessions need credentials during active work. Long-lived credentials stay outside context. |
| A28 | Provide API views of each task, its agents, their context history, tool results, and human actions. | Operators need evidence to inspect and improve the system. |
| A29 | Joining an environment is live. Pause, resume, and command cancellation are separate requested operations. | Attachment must not change execution control implicitly. |
| A30 | Use explicit user messages to inform the initiating persona of changes. Do not inject file-watcher notifications into agent context. | The persona decides what additional context is useful to the worker. |
| A31 | Let Magi investigate failures and decide retention, further work, or human involvement per task. Verify assumptions with evidence. | Tasks are not limited to code or processes. One cleanup rule cannot cover them all. |
| A32 | Do not retain historical secret values for replay. Keep references and relevant issuance metadata. | Specialized historical-value needs belong outside Magi. |
| A33 | A persona has a stable LinkKeys identity, voice, personality instructions, and focuses. Users can share it. | Configuration changes preserve identity; replacement creates a different persona identity. |
| A34 | Workers use the responsible persona's identity. Session, task, agent, and attempt IDs are internal records. | Temporary LinkKeys identities are outside scope, not a required future feature. |
| A35 | A session bounds one task, its interactions, and child work. A conversation can contain several sessions. | A new task needs separate context even when the visible conversation continues. |
| A36 | Start with persona privacy defaults. Broaden only when permitted. Source restrictions constrain the whole session and further analysis. | A private source overrides a public starting scope, including other steps and children. |
| A37 | Shared personas access private or group context on behalf of the requesting user. | Sharing a persona does not combine users' permissions. |
| A38 | The Magi administrator can evaluate retained evidence under its own authority. Classify later learnings by their sources and task scope. | Ordinary context restrictions remain; uncertain learnings stay restricted. |
| A39 | Humans edit trust policy only within their control areas. Separate receipt policy from processing policy. | Household authority does not grant control over another person's private trust choices. |
| A40 | Inject admitted credentials directly when tools need them. Do not require one universal proxy or credential-delivery method. | SSH keys, JWTs, and other credentials have different tool requirements. |
| A41 | Compose evaluation context from the system evidence graph, outside the proposing worker's context. | Scope evaluation uses relevant evidence and policy, not the worker's own approval. |
| A42 | Connect different source and storage types with relationship metadata and original evidence. Do not require one graph or node hierarchy. | Documents, records, media, and scoped indexes can have different representations. |
| A43 | Magi arbitrates knowledge through its APIs. Queries need an authenticated identity and scope, not necessarily a persona or represented human. | Systems can act for an authorized household, company, or domain under their own authority. |
| A44 | Preserve communication with context, supporting and contrary evidence, and uncertainty. Do not treat conversation as authorship or permanent truth. | Intent, time, place, mood, and missing context affect interpretation. |
| A45 | Give greater weight to knowledge authored by the user or an authority they accept for the item or subject. Let authors choose persona assistance and review. | Publication does not establish authority in someone else's context. |
| A46 | Accept personal knowledge within the adopting context. Permit direct acceptance of system and deterministic knowledge. | A user's belief about Alex does not need Alex's approval or become authoritative for Alex. |
| A47 | Respect implied source purpose and explicit enrichment settings. Private journals are not routine enrichment sources. | Read access alone does not authorize every use. Relevant live calendar context can be useful. |
| A48 | Allow unresolved conflicts and authorized invalidation or deletion. Authority can include an owning group or applicable organizational hierarchy. | Disagreement does not authorize changes to another person's document. Detailed deletion behavior remains open. |
| A49 | Match query precision and collection effort to purpose and context. Support estimates, comparisons, and reproducible calculations. | Missing sources or inconvenient MFA need not prevent a useful exploratory answer. |
| A50 | Coordinate simultaneous conversations and sessions across voice and text in real time. | A spoken question can receive a typed reply, while other exchanges continue. |
| A51 | Permit one response to advance several sessions. Preserve its source, interpretations, and actual destination inputs. | Splitting or sharing a response must not erase its common origin or merge privacy boundaries. |
| A52 | Permit interaction modes specific to identity, interaction point, situation, or their combination. | Settings and contextual learning can improve future interpretation without one fixed behavior for every user. |
| A53 | Preserve linked evidence layers for later reinterpretation. Allow more layers and context-specific assessment weights. | Reprocessing, new evidence, or review can improve understanding without rewriting earlier inputs or decisions. |
| A54 | Target recall within a few seconds, or acknowledge promptly and continue for about twenty seconds. Expose blocking service failures. | Historical context must be useful during live interaction, including quiet hours. Targets still need measurement. |
| A55 | Support explanations of actual non-actions and source-based technical walkthroughs. | Recorded inputs and decisions must remain distinct from later explanations or illustrative examples. |
| A56 | Give workflows an independent service, API, and definition model. Longhouse does not own them. | Workflows span services and can run without a project, persona, or conversation. A graph editor and other clients use the same model. |
| A57 | Keep the current Go services and browser client. Use Python for agent execution. | The user accepted this language boundary. CSIL separates services from execution implementations. A65 now records the requested initial harness. |
| A58 | Use a small local model for the first question and answer setup and CI checks. | The user will add a remote model API after testing this path. A deterministic responder can test faults, but it cannot replace the real-inference check. |
| A59 | Implement Android as the first mobile client. | Selected by the user on 2026-10-15 for voice and notifications. |
| A60 | Offer regular application and optional default-assistant modes on Android. | Selected by the user on 2026-10-15. Neither mode guarantees unrestricted microphone access. |
| A61 | Integrate locally hosted compatible calendaring, like Longhouse, through replaceable adapters. Keep the new calendar service and extraction separate. | Confirmed by the user on 2026-10-15. The user will build the calendar project separately; Magi does not require a remote calendar provider. |
| A62 | Provide at least eight voices through a replaceable TTS contract. Compare alternatives to Breeze. | Revised on 2026-10-16. CUDA is a permitted engine constraint, not an Magi-wide requirement. A replacement engine has not yet been selected. |
| A63 | Estimate speakers per person and capture device for graph attribution and cross-talk handling. Use policy-selected authentication for sensitive operations. | Clarified on 2026-10-16. Simple requests need no blanket MFA. LinkKeys authenticates; receiving services authorize. Personal TTS cloning is excluded. |
| A64 | Demonstrate useful interaction beyond Q&A and develop Magi through Magi. | The user accepted the connected demo direction on 2026-10-16. A68 replaces the proposed conversation-mode catalogue with a graph-first model. |
| A65 | Build an Magi-owned Python harness in this repository from the MIT-era lo-agent lineage and lessons from OpenCode v2 and Pi. | Revised on 2026-10-16. Keep Python for ML integration. Record source provenance and notices; current restricted lo-agent is not the runtime dependency. |
| A66 | Evaluate forkable open-source microVM and container mechanisms, including Microsandbox and Lima. Use mise for environment tools and packages. | Requested on 2026-10-15. The exact backend and version remain open; native and exact-host requirements remain. |
| A67 | Start speech deployment on `192.0.2.20`. Permit separate STT and TTS services on shared or additional hardware. | Clarified on 2026-10-16. The experimental service remains separate. Hardware placement is engine-specific. |
| A68 | Use the conversation graph as the primary interaction model. Modes are optional interpretations of overlapping segments. | Confirmed on 2026-10-16. Knowledge derives from the graph and other evidence sources; conversation types are the wrong primitive. |
| A69 | Process retained voice locally with two or three interchangeable transcription engines to support earlier audio deletion. | Confirmed on 2026-10-16. Retain candidates, disagreements, and the original live context. Consensus does not establish truth. |
| A70 | Detect speech failures, select permitted alternatives, or send text with failure evidence and involve a human. | Confirmed on 2026-10-16. Failure handling must not depend on the failed engine. |
| A71 | Provide automatic rollback when a new Magi version fails. | Confirmed on 2026-10-16. Keep independent recovery, known-good artifacts, compatible schema changes, and effect reconciliation. |

## Proposed defaults

| ID | Proposal | Validation |
| --- | --- | --- |
| P01 | Start with one modular coordinator and separate host executors; support coordinator replicas. | Prove one instance first, then failover without duplicate commands or lost human holds. |
| P02 | Use PostgreSQL for Magi metadata, receipts, and publication records. | Demonstrate transaction and restore behavior. Corndogs retains task-state ownership. |
| P03 | Use direct APNs/FCM adapters first. | Compare operating cost and device-token management with a managed provider. |
| P04 | Map a documented Open Workflow subset to CSIL. | Preserve meaning through import, edit, export, and execution. |
| P05 | Use durable inbox/outbox records at service boundaries. | Kill processes at each commit and acknowledgement boundary. |
| P06 | Preserve event bodies across retries. Authenticate delivery attempts separately. | Accept a valid delayed event without repeating its effect. |
| P07 | Enforce protected operations at the resource service. | A request that bypasses an Magi client remains subject to the same rules. |
| P08 | Use environment holds and versioned control assignments. | Test concurrent human work, plan replacement, and cleanup after explicit release. |
| P09 | Store secret substitution bindings as typed metadata beside reference-form payloads. | Local capture and Go/Rust tests pass for literal references, rotation, equal values, and stream boundaries. Authenticated producer admission and remote upload remain open. |
| P10 | Test PostgreSQL records and text search with a payload store and live-source adapters; compare optional pgvector retrieval. | Approved as an experiment starting point. Local semantic comparison favors combined retrieval with evidence links on a small corpus. Payload storage, live adapters, scale, and vector index selection remain open. |

## Open choices and experiments

The [integrated release design](integrated-release.md) supplies implementation
defaults for the first connected release. It selects embedded CEL, the existing
database-backed event path, direct FCM for Android, and scoped storage interfaces.
These are implementation choices, not new claims of user approval or passing
integration tests. Device measurements and external setup remain release gates.

| ID | Choice | Required evidence or owner |
| --- | --- | --- |
| O02 | Policy integration | Embedded CEL is the initial implementation choice. Typed CSIL bindings, service-local composition, and fail-closed integration still need tests. |
| O03 | Event transport validation | Use the existing database-backed pull path initially. NATS remains an optional transport. Test the connected workflow and restore path. |
| O04 | Workflow conversion | Target Open Workflow v1.0.3 at the pinned commit in the release design. Native CEL and imported expressions require explicit semantic mappings. Test round trips; do not claim full conformance. |
| O08 | Allowance withdrawal and freshness | New protected effects require fresh withdrawal evidence. Define and test numeric bounds per operation; do not treat key revocation as allowance withdrawal. |
| O09 | Notification deployment | Implement direct FCM for Android and authenticated inbox fetch. Live tests need a dedicated provider project and device. |
| O10 | Local calendar integration | Use locally hosted compatible calendaring through an adapter. The native calendar design and extraction belong to the user's separate project. |
| O11 | Speech models and device runtimes | Compare Qwen CustomVoice, Kokoro, Pocket, and optional Chatterbox. Pin runtime, weights, and eight permitted voices. Test backend placement and measured quality. See the prototype design and Parakeet smoke result. |
| O12 | Recovery and limit validation | The release design sets initial configurable lab limits and backup targets. Measure them before unattended work; these are not existing guarantees. |
| O13 | Stable identity bootstrap | Use each test installation's LinkKeys integration. A new production domain is not an implementation prerequisite. Complete persona and host enrollment, rotation, and recovery without temporary identities. |
| O14 | Component extraction and host executor implementation | Select reusable Reactorcide code, library boundaries, native process controls, and supported sandbox backends. |
| O15 | Evidence integration | Use sanitized bounded spools, explicit gaps, and separate late-upload permission. Refuse unresolved current inputs. Prove this through real executors; historical secret retention is excluded. |
| O16 | Knowledge retrieval validation | Start with service-local PostgreSQL records, replaceable payload storage, and scoped combined retrieval. Test a stronger evaluator and realistic scale. The small-model support check does not pass. |
| O17 | Interaction adaptation and assessment detail | Define only the initial records and links. Defer mode learning, weight scales, reflection schedules, and mood or tone models. |
| O18 | Knowledge deletion validation | Remove access first, invalidate derived indexes, propagate origin changes, and retain permitted tombstones. Test cleanup, incomplete propagation, and restore before private-data use. |
| O19 | Internal Python harness | Review and record MIT-era source imports. Define graph-context projections, steering, tools, receipts, compaction, and pre-write capture. |
| O20 | Speech authentication and retention | Specify sensitive-operation challenges and required LinkKeys evidence. Define local transcript agreement, bounded audio retention, and deletion. |
| O21 | Graph and self-development contracts | Define overlapping segments and optional analyses, separate controls, release health checks, and automatic rollback. The demonstration direction is accepted. |

O01 was resolved and corrected by the user on 2026-09-27: `catalystsquad.local`
is primary and `partner.local` is secondary. Catalyst Community development
integrations use example as their LinkKeys domain when they use one.
The partner system also has its own LinkKeys integration. It is not a
client of the example installation. This choice does not migrate existing
integrations to partner. Live DNS
configuration and deployment have not been verified by this design work.

O05's service contract was resolved by the 2026-09-29
[Corndogs 0.8.0 tests](corndogs-recovery-080.md). Use keyed submissions and
guarded operations under the [adapter contract](corndogs-adapter.md).
W05 must still prove durable intent and external-effect recovery in Magi.

O07 was closed by the 2026-10-05 interview. Use stable persona identities for
workers. Temporary task or agent identities are not an implementation gate.

Do not mark an open choice accepted merely because an example uses it.
Update this table when an experiment resolves a choice. Record the evidence.

## Next Actions

Close the [prototype design gates](demo-prototype.md), then connect W02's tested
approval and cross-domain checks to sessions, capture, knowledge, and execution.
Preserve the approved knowledge and persona boundaries. Use the integrated
design for the wider release. Do not treat component results as demo completion.
