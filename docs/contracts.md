# Native contracts

Status: field and operation inventory with a compiled W03 baseline in
[csil/magi.csil](../csil/magi.csil). The [native protocol](native-protocol.md)
defines its assigned IDs, limits, and tests. The larger API inventory below
includes future operations that have no runtime handler.
Expanded 2026-10-06 for knowledge queries and concurrent interaction. Initial
records must permit later assessment detail without requiring a full learning model.
The [2026-10-09 storage proposal](knowledge-storage.md) defines initial field
groups, admission rules, and a shared-reply example for these records.

The [2026-10-11 experiment](knowledge-experiment.md) compiles a minimum subset
under `experiments/knowledge/schema/`. Go and Rust codec checks pass for that
subset. Its service declarations have no runtime endpoints. The complete
inventory below remains broader than that experiment.
The [W02 service experiment](identity-service.md) adds compiled approval
records, Go/TypeScript checks, and working prototype CSIL endpoints.

## Shared values

| Type | Fields and meaning |
| --- | --- |
| IdentityRef | Canonical account ID and identity domain; optional display handle outside equality |
| ApplicationRef | IdentityRef, application ID, instance ID |
| ResourceRef | Authority identity, resource kind, stable resource ID |
| RevisionRef | ResourceRef, revision, content digest |
| SchemaRef | Schema identifier, version, descriptor digest |
| OperationRef | Receiving service identity, service name, operation name, schema revision |
| TimeWindow | Start instant and optional end instant; absent end means no stated expiry |
| ArtifactRef | Authority, immutable object ID, digest, byte size, media type |
| CauseRef | Record kind, authority, record ID; support multiple causes |
| SecretRef | Secret authority, path, and key; display with `${secret:path:key}` notation |

Use CSIL timestamp and decimal types where applicable. Store recurring local
times with an IANA timezone and a recurrence rule. A UTC instant is insufficient
for a recurring local appointment. Include units for quantities and currency
for amounts. Preserve exact values through policy and protocol adapters.

## Signed delegation allowance

An allowance is a statement by an issuer. A receiving service decides whether
the issuer has standing and what the statement permits. LinkKeys does not make
that authorization decision.

| Field | Rule |
| --- | --- |
| allowance_id | Stable ID under the issuer's authority |
| issuer | Identity that makes the statement |
| delegate | Identity allowed to represent the issuer |
| purpose | Versioned vocabulary, such as a calendar delegation purpose |
| attributes | Payload governed by SchemaRef; do not treat arbitrary text as executable policy |
| validity | TimeWindow; indefinite duration is supported |
| audience | Optional recipient or service restrictions defined by the allowance schema |
| issued_at | Stated issue time; historical verification follows the signature protocol |
| supersedes | Optional prior allowance reference |
| withdrawal_ref | Where an authenticated withdrawal status can be obtained |

Bind signature context, schema, and body bytes. Preserve the signed bytes.
Use existing LinkKeys signing and verification facilities where they apply.
Define who signs as the issuer when an application prepares the statement.
An application key attestation alone does not prove that a human approved an
arbitrary statement. The user selected authenticated approval of exact
allowance bytes, recorded by Magi, on 2026-09-30.

ApprovalRecord needs the canonical human identity, allowance digest, approval
service identity, approval time, and authentication evidence reference.
The backend obtains the identity from the authenticated session. It must not
accept a client-supplied identity or approval Boolean as proof. Preserve the
approved statement and evidence; require new approval when its bytes change.
Foreign services must authenticate approval evidence and decide whether they
trust its source. The [W02 service report](identity-service.md) records real
authentication, durable approval, and cross-domain verification. Production
session, storage, and federation controls still need integration.

An accepted allowance can outlive its application's signing key. Retain the
verified statement and approval. Authenticate each new request with current
evidence. Apply allowance expiry, withdrawal, and receiver policy separately.

A Withdrawal record names the allowance and effective time. Authenticate its
issuer under the allowance rules. Record replacements and withdrawals without
rewriting the original statement. Allowance withdrawal, key revocation, and
application session logout are distinct operations.

The receiving service supplies verified facts to its policy evaluator. These
include issuer, attribute signer, subject, freshness, requested operation, and
resource ownership. A valid third-party signature does not establish that the
service trusts that signer for that attribute.

Policy output includes allow or deny, local policy revision, evidence
references, and a machine-readable reason. The service enforces the result at
the protected operation. Magi's prior decision is not sufficient by itself.

## Persona identity and session records

The persona has a stable LinkKeys IdentityRef. Its workers use that identity.
Separate internal IDs identify conversations, sessions, tasks, agents, steps,
and attempts. They are evidence and coordination IDs, not temporary LinkKeys
identities. Temporary identity enrollment is outside scope.
A persona signature alone does not identify one worker. Check internal actor
IDs against coordinator and executor admission records, not just caller claims.

A persona revision changes voice, personality instructions, focuses, or
defaults without replacing the identity. A replacement persona has a different
identity. Shared personas preserve each represented user's context permissions.
Resource services retain final authorization. See
[Personas sessions and trust](personas-sessions-and-trust.md).

| Record | Required fields or meaning |
| --- | --- |
| Persona | Stable persona ID, IdentityRef, current configuration revision, subsystem assignments |
| PersonaRevision | Voice configuration, personality instructions, focuses, privacy defaults, author, prior revision |
| Session | Internal ID, conversation links, task boundary, actor, applicable scope, optional persona and represented identity, child links, effective restrictions |
| ContextAccess | Authenticated actor, applicable authority and scope, optional persona and represented identity, source references, purpose, result audience, processing destination |
| PrivacyScope | Source owners, audience restrictions, policy references; preserve distinct private and group authorities |
| ScopedTrustPolicy | Control area, policy editors, recipient or processor, permitted data and uses, revision |
| EvaluationRecord | Proposed operation or learning, selected graph evidence, context snapshot, scope and policy revisions, evaluator, decision, uncertainty |
| LearningAssessment | Candidate interpretation or learning, input revisions, classification, applicable scope and permitted audience, assessor and method, evidence references; optional typed dimensions and context-specific weights |

A new task starts a separate session, even within one conversation. A change
in attention can return to an existing session without starting another task.
Child work contributes to the parent session's privacy. Do not copy context
between sessions without permitted retrieval. Persona defaults can broaden
only where source restrictions and policy allow it.

The administrator evaluates retained Magi evidence under its own authority.
That authority does not bypass source processing policy. User work accesses
private and group context on behalf of the represented requesting user.
Other system work can use the caller's own household, company, or domain
authority. Neither a persona nor a represented human is mandatory for a query.

Build evaluation context from the graph outside the proposing worker. Preserve
the evaluated operation and evidence revisions. Do not use the worker's own
approval as the scope decision. These records do not require a new service
process or identity for each evaluator.

## Knowledge and interaction records

These records describe relationships across stores. They do not require one
graph, a fixed node hierarchy, or one representation for every source. Existing
ArtifactRef, RevisionRef, and CauseRef supply source links where applicable.

| Record | Initial meaning |
| --- | --- |
| SourceContext | Source authority and references, available representation, observation time, source-use settings, privacy and processing restrictions |
| KnowledgeQuery | Caller, scope, optional represented identity and persona, purpose, question or typed request, relevant periods, precision and effort context |
| QueryResult | Answer or range, sources and observation times, calculations or interpretations used, coverage, uncertainty, retrieval status |
| KnowledgeAcceptance | Adopting context, authorized accepting actor, item or subject scope, accepted claim or author authority, evidence reference |
| InteractionRecord | Source input and available context, identity evidence, interaction point, channel, times and sequence, payload or media references |
| InterpretationRecord | One or more source references, context used, proposed associations with prompts and sessions, method, uncertainty, prior interpretation or correction link |
| SessionInput | Destination session, source and interpretation references, actual admitted content, admission receipt, separate delivery and context-use evidence |
| PresentationRecord | Interaction point, related prompts and outputs, observed presentation event, sequence, time, and evidence or uncertainty |

One input can have several destination sessions. Each destination retains its
actual content and receipt. Shared meaning need not map to a single text span.
A durable admission is not proof of delivery or use. Link destination receipts
and actual context snapshots without rewriting the admitted content.
A conversation can use several interaction points; an interaction point can
serve several conversations. These are not one-to-one foreign-key assumptions.

Keep original evidence, earlier interpretations, and delivered inputs distinct
from later assessments. Additional typed assessment dimensions can be added
through versioned schemas. Their presence is optional unless the negotiated
operation requires them. Do not fix one confidence scale, intent taxonomy,
mode model, or reflection schedule. A new assessment grants no execution rights.

Personal acceptance is scoped to the adopting context, not imposed by or on
the person described. Source access, enrichment, authorship, acceptance, and
deletion require distinct policy decisions. Deletion and invalidation require
authority over the affected resource. Their detailed lifecycle remains open.
See [Knowledge context and reinterpretation](knowledge-and-context.md).

## Command and result

Command: command ID, actor, represented identity when applicable, OperationRef,
resource references, allowance references, expected resource revision, typed
input, deadline, and causal references. Include session, internal agent and
attempt IDs, and the applicable tool admission or evaluation reference.

The recipient binds the idempotency key to the authenticated actor, operation,
and input digest. A retry with the same key and different input is a conflict.
Store the result or an outcome-unknown record before accepting further action
that would depend on an assumed result.
For secret inputs, bind command equality to the admitted reference form,
binding IDs, and available version metadata. Do not publish a hash of
secret-bearing bytes. Credential replacement changes the binding.
Record that change explicitly.

Receipt states distinguish durable acceptance, execution completion, rejection,
and unknown outcome. A transport response is not proof of business completion.

## Environment and control records

These records have initial W03 wire shapes. Their runtime admission and
lifecycle rules remain implementation work. See the
[execution design](execution-environments.md) for enforcement requirements.

| Record | Required fields or meaning |
| --- | --- |
| EnvironmentDefinition | Revision, platform, immutable image or bundle, pinned tools, configuration, supported mechanisms |
| EnvironmentRequest | Exact host or capabilities, definition, mechanism, bindings, persona identity, applicable scope, optional represented identity, session, allowances, limits, lifetime policy |
| ResourceBinding | Host-local name, resource kind, permitted subpaths, access mode, sharing rule |
| EnvironmentInstance | Admission receipt, resolved host and definition, assignment generation, active uses, holds, cleanup status |
| ControlAssignment | Environment, controller identity, shared or exclusive mode, affected resources, generation, validity |
| HumanHold | Environment, holder identity, admission record, creation time, release receipt or explicit forced-end record |
| PlanRevision | Prior revision, author, changed steps, retained or cancelled commands, uncertain outcomes, authorization evidence |
| CommandOperation | Command fields, environment, control generation, argument or shell form, stream IDs, admission and result receipts |
| InvestigationRecord | Failed task or attempt, observations, assumptions, checks, remaining uncertainty, retained resources, chosen next action |

Environment lifetime is independent of task state. Human release and normal
cleanup need atomic admission rules. A plan revision does not extend authority.
Separate process exit, effect outcome, evidence export, and cleanup status.
Native processes and host mounts must declare their actual access limits.
Joining is live. Pause, resume, and cancellation are independent requests.
Do not make joining or a lifetime hold perform any of them. Failure handling
uses task-specific investigation and policy, not one automatic cleanup rule.

## Secret bindings and evidence records

| Record | Required fields or meaning |
| --- | --- |
| SecretBinding | Binding ID, SecretRef, available version or issuance metadata, session scope, validity, resolution policy; no value or historical-value retrieval promise |
| EvidenceRecord | Authenticated actor, applicable scope and optional persona, session or interaction links, task, agent, attempt, and environment where applicable, type, schema, sequence, causes, payload reference, capture status, source restrictions |
| SubstitutionMap | Typed payload locations, binding IDs, supported transformation, ambiguity status, capture version |
| ContextSnapshot | Session, context purpose, ordered input and sources, tool schemas, persona and plan revisions, effective restrictions, retained content or lossless delta bases |
| AgentParticipation | Agent instance, task and attempt, session, context links, handoff causes, access policy |
| StreamManifest | Producer, stream ID, expected end, accepted sequence ranges, gaps, artifact references, publication receipts |
| ReplayRequest | Playback, current-input preparation, or new execution mode; source records, destination, current authorization, new credential bindings |

Substitute values before durable archive storage, local spooling, indexing, or
general observer delivery. A permitted short-lived credential can remain in
active context for that session. Persist its reference instead. Keep values in
the secret service under a separate policy. Historical secret values are not
retained for replay. Direct injection of admitted credentials is supported;
scope review does not itself restrict what a broad credential can do.

Secret segments are typed data. Literal reference syntax in tool output must
not trigger resolution. Preserve authority, binding ID, and available version
metadata outside the familiar display notation. Ambiguous references cannot
be silently resolved. Archive digests identify the stored representation,
not an original secret-bearing stream. See [Evidence archive](evidence-archive.md)
for signed-input handling.

History queries never resolve secrets or repeat effects. Input preparation
resolves current credentials under current policy. It is not historical-value
reconstruction. A new execution receives a new internal attempt ID. Record its
credential bindings and changed inputs without rewriting the original evidence.

## API inventory

The W03 subset has assigned names and wire IDs. Other names in this inventory
remain design names. Only discovery is enabled in the deployed service.

| API | Initial operations | Authorization owner |
| --- | --- | --- |
| Registry | Register, RenewPresence, Describe, FindCapabilities | Registry service |
| Allowance | PublishStatement, ReadStatement, PublishWithdrawal | Statement host and each consumer |
| Federation | DescribePeer, ProposePeer, AdmitPeer, Deliver, GetReceipt | Each receiving domain |
| Subscription | Create, Update, Revoke, ReadFromCursor, Acknowledge | Event service |
| Workflow | Validate, SaveDraft, PublishRevision, Start, Inspect, Cancel | Workflow service |
| Task | Read, SubmitResult, ReconcileAttempt | Workflow service and Corndogs adapter |
| Inference | DescribeCapabilities, Generate, Stream, Cancel | Inference adapter |
| Environment | Describe, Create, Inspect, Hold, ReleaseHold, RequestClose | Magi admission and receiving host executor |
| Execution | StartCommand, ReadStatus, SendInput, ReadOutput, Cancel, AttachShell, ImportFiles, ExportFiles | Environment executor under current control and resource policy |
| Persona | Create, Read, PublishRevision, Replace, AssignSubsystem | Magi persona administration and resource policies |
| Session | Begin, SubmitInput, StartChild, ReadContext, End | Magi under caller authority, applicable defaults, and source policies |
| Agent | ReadParticipation, SendContext, Pause, Resume | Magi under the responsible persona and task policy |
| TrustPolicy | Read, Update | Authorized policy editors within the specified control area |
| Evaluation | ComposeContext, EvaluateToolUse, AssessLearning, ReadDecision | Magi evaluation under source privacy and processor trust |
| Plan | ReadRevision, ProposeRevision, AdmitRevision | Magi workflow and resource admission |
| Secret | ResolveBinding, IssueSessionCredential, Renew, Revoke | Secret authority and credential issuer |
| Evidence | ListTaskHistory, ReadAgentHistory, ReadInteractionHistory, ReadAssessments, ReadContext, ReadStream, FollowCauses, CompareAttempts | Magi evidence authority with per-record and payload checks |
| Replay | ReadPlayback, PrepareCurrentInputs, StartNewAttempt | Evidence authority plus current secret and execution authorities |
| Knowledge | Query, ReadQuery, Explain, ReadRevision, ProposeChange, CommitChange, ReadChanges, AcceptForContext, Invalidate, Delete | Magi and the authority for each source resource or acceptance context |
| Notification | RegisterEndpoint, SetRules, ListInbox, MarkSeen, Acknowledge | Recipient's notification service |
| Speech | Describe, BeginRecognition, SendAudio, Finish, Synthesize, Cancel | Speech service |
| Conversation | Open, AttachClient, SendInput, Close | Conversation service; task session boundaries remain explicit |
| Interaction | SubmitInput, ReadPending, ReportPresentation, CorrectAssociation | Magi under caller, source, destination, and interaction policy |
| Audit | ReadRecord, FollowCauses, InspectDecision | Evidence authority |

Each list API needs stable pagination and access filtering. Each write API
needs a concurrency rule, size limit, and error classification. Streaming APIs
need cancellation, flow limits, and a reconnect rule. Define which operations
work anonymously; do not infer this from the absence of a credential.
Treat client routing hints and presentation reports as attributed observations,
not authority to expand access or proof that a person understood an output.

## Protocol evolution

Use generated CSIL codecs and descriptors. Pin generator and transport versions.
Keep ordinal values stable. Do not reorder positional union variants without a
compatible version change. Test old and new clients before upgrading a service.

Negotiate supported schemas before execution. Store the schema reference with
durable payloads. Decode old records with their original schema. Unknown
required semantics must cause rejection, not silent omission.

CSIL-RPC carries commands and queries. CSIL-Events carries live typed streams.
The durable delivery protocol adds receipts and resume cursors above either
carrier. CSIL-Datagrams can carry loss-tolerant audio or observations when the
application defines acceptable loss. Do not put durable obligations solely on
a datagram stream.

## Next Actions

Review IdentityRef, ResourceRef, Command, Event, Receipt, Allowance, and
ApprovalRecord with persona, session, trust, environment, and evidence records.
Use the [storage proposal](knowledge-storage.md) to build fixtures for the minimal
source, interpretation, and session-input links before adding learning dimensions.
Test multi-destination inputs and later reinterpretation without fixing a
universal storage model.
Build byte-level signature, secret-substitution, and codec fixtures before
adding UI-specific data. Keep proposed operation names separate from
implemented capabilities.
