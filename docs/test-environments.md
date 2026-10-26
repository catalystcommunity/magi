# Test environments and acceptance

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

Status: acceptance plan with a deployed primary discovery service. The complete
two-domain workflow remains open. See the [deployment procedure](deployment.md).

Update 2026-10-15: the separate [protected Q&A deployment](question-answer-example.md)
now passes real LinkKeys authentication, model execution, evidence retrieval,
and API replacement checks. The [integrated release design](integrated-release.md)
defines the full connected acceptance gate. The earlier component reports below
remain evidence for their stated boundaries, not proof of the complete release.

On 2026-10-13, W03 added two read-only Magi replicas in `magi-example` at
`https://magi.catalystsquad.local/`. Public Go and TypeScript probes pass. The route
uses the existing gateway and router TLS. LinkKeys and other workloads were
not changed. No protected operation or persistent Magi database is enabled.

W04 adds a separate local event service. It does not change that deployment.
Its tests use independent LinkKeys servers and event databases in disposable
containers. External consumers use their own SQLite stores. See the
[event service guide](event-service.md) for the protocol and recovery checks.

## Domain roles

| Environment | Identity and resources | Purpose |
| --- | --- | --- |
| Disposable A | primary.opal.test | Automated primary authority |
| Disposable B | secondary.opal.test | Automated independent peer |
| Persistent primary | catalystsquad.local | Orchestration and established Catalyst Community LinkKeys integration |
| Persistent secondary | partner.local | Separate system with its own LinkKeys integration and resource authority |
| Controlled external participant | User's separately configured identity domain | User-driven access to test resources |

The two disposable authorities need independent keys, trust configuration,
resource policies, and data. They can share a physical test host. Use the
supported LinkKeys discovery and trust facilities with test DNS and test keys.
Do not disable signature or audience checks to make federation tests pass.

Install two independent systems. Each has its own LinkKeys integration,
identity authority, service registrations, resource policies, and data.
Do not use one identity installation behind two domain names. The systems
can use different service versions and configurations to test compatibility.

The persistent primary is a test environment. It is not a global owner of the
secondary. Run workflows in both directions. Reject unapproved requests in both
directions. Test cooperation between identities from different authorities.

## Persistent service set

| Service | Primary | Secondary |
| --- | --- | --- |
| LinkKeys IDP/RP | Existing example deployment and local service integrations; inspect before reuse | Separate partner deployment and local service integrations |
| Magi API and event adapter | Primary workflows and inbox | Peer admission, secondary workflows and inbox |
| Corndogs | Primary task state | Secondary task state |
| Metadata and artifact storage | Separate database and namespace | Separate database and namespace |
| Longhouse | Existing example test instance; inspect before reuse | Second test instance when required by the scenario |
| Magi host executor | Approved native and container test targets; add Windows and macOS VM targets | Independent admission policy and bindings for foreign execution tests |
| Secret service and evidence archive | Primary bindings, capture records, and task history | Separate credentials, archive access, and retention policy |
| Speech and devices | Add after text workflow tests | At least one independent endpoint when tested |
| Telemetry | Labels and access for the primary | Labels and access for the secondary |

Reuse shared infrastructure only where configuration and credential boundaries
remain enforceable. A namespace alone is not a complete resource boundary.
Separate database roles, artifact access, provider applications, and job secret
grants. Persistent tests use synthetic documents, calendars, and task content.

The agent-learning notes describe existing LinkKeys and Longhouse example
deployments. They are historical evidence, not a live health check. Inspect
the installed versions, data, and ownership before making deployment changes.
Keep existing Catalyst Community LinkKeys integrations on example. Add
partner as a separate peer authority, not a replacement identity domain.

## User preparation

The user selected `partner.local` for the secondary domain. Identify its DNS
management path and confirm domain control before deployment. After the endpoint design is fixed,
prepare DNS, TLS, and LinkKeys discovery for that domain. Create a test identity
and a service identity under the new domain.
Provision infrastructure through the established Foundry/Reactorcide flow.

Do not copy production identity-provider secrets, resource credentials, or
private data into the test stack. A production identity can authenticate to a
test resource without granting test agents access to production resources.

Separate public application signing from test credentials. Push tests initially
use a provider stub. Real device tests use a dedicated application registration
and a user-controlled device. Audio capture requires explicit device consent.

## First complete scenario

Use a synthetic household or project on the secondary domain.

1. An identity on the primary signs a statement permitting a test assistant
   to represent it for the declared test purpose.
2. A secondary service validates the identity and statement, then applies its
   own resource policy. An allowed command starts a workflow.
3. An automated step runs a small command through an Magi test executor with
   test data and no production secrets. Its result produces a durable event.
4. A human step waits on the secondary. An optional scenario creates a Longhouse assignment.
5. An authorized identity responds directly or completes the bound assignment.
   The workflow advances once.
6. Two branches run and join under an explicit rule.
7. A completion notification enters the primary identity's inbox. A virtual
   clock holds it until the next configured business morning.
8. The audit query shows the signed evidence, both service decisions, task
   references, branches, delivery receipts, and final result. The full execution
   test adds agent context, tool output, and reference-only secret segments.

Repeat with reversed domain roles. Repeat with a denied allowance, withdrawn
allowance, untrusted attribute signer, and unavailable worker.

## Automated test matrix

Measured update, 2026-09-29: Corndogs 0.8.0 passes the service-boundary checks
for T08/T09 across five file and PostgreSQL configurations. See
[the recovery report](corndogs-recovery-080.md). Full T08/T09 require Magi
journal and external-effect tests in W05. Other rows remain acceptance
requirements, not claims of passing tests.

W02 update, 2026-09-30: T01/T23/T24 pass at the local SDK and policy-fixture
boundary. The [identity and policy report](identity-policy.md) records 495
passing checks across three runs. Real LinkKeys authentication, durable human
approval, and cross-domain approval evidence were untested at that date.

W02 update, 2026-10-13: the [service experiment](identity-service.md) adds 102
leaf passes across three runs, without skips. T01/T23/T24 now have real login,
remote key resolution, durable approval, and cross-domain evidence checks in
disposable systems. Production deployment and the full workflow remain open.

W04 update, 2026-10-14: T02 through T05 pass for the standalone event API and
local transaction helpers. T06 tests the retained unknown-outcome state and
explicit reconciliation; a real external business provider is not involved.
T07 tests an event with a two-hour-old occurrence time and a fresh request proof.
It does not measure a two-hour network outage. Process and database restart
tests do not establish backup-restore or database HA guarantees.

Further W04 tests now use real event-store backup and restore. They cover
generation fencing, receipt comparisons, lost acknowledgement state, and
explicitly accepted gaps. They do not perform automatic cross-store repair.
The service also tests concurrent quota admission and version 1 database migration.

W05 tests now cover T08/T09 at the journal and bounded local-effect boundary.
They use file-backed Corndogs and two servers over PostgreSQL. Internal human
bindings test duplicate completion and source matching. The independent service
also tests direct human responses without Longhouse. The optional Longhouse
boundary remains open. T11 still needs worker registration and blocked-status reporting.
See the [workflow journal](workflow-journal.md) for the exact limits.

Knowledge update, 2026-10-11: the [synthetic storage experiment](knowledge-experiment.md)
tests parts of T53, T54, T58, T60, and T67 through T70. Its codec, admission, privacy,
and local recovery checks pass. Text retrieval fails the proposed recall target.
The tests do not complete these acceptance rows. They use fixture authority,
supplied interpretations, and one database with two namespaces, not two
independent authenticated systems. T71's alert and quiet-hour scenario is open.

Retrieval update, 2026-10-12: the [local comparison](retrieval-comparison.md)
adds CPU model inference and tests six methods on a small synthetic corpus.
Two test runs produce identical candidates. This adds evidence for permitted
retrieval and record-link expansion; it does not complete T70 or T71.
Answer support, device latency, scale, and independent domains remain open.

| ID | Test | Required result |
| --- | --- | --- |
| T01 | Valid signature, issuer lacks resource authority | Service denies the operation |
| T02 | Same event delivered repeatedly | One local effect; stable acceptance receipt |
| T03 | Same command key, different input | Explicit conflict; no second effect |
| T04 | Kill producer after state commit | Outbox publishes the committed event after restart |
| T05 | Kill receiver after inbox commit, before reply | Retry obtains the same receipt |
| T06 | Lose an external-operation reply | Reconcile or expose unknown outcome; no blind retry |
| T07 | Disconnect domains beyond one hour | Valid retained obligations recover after reconnect |
| T08 | Expire worker claim, then submit old result | Old attempt cannot complete the new attempt |
| T09 | Lose Corndogs submission reply | Recover the logical step without duplicate business work |
| T10 | Duplicate human response or external assignment completion | Advance the bound human step once; test each enabled adapter |
| T11 | Automated step has no workers | Show blocked status, not a human assignment |
| T12 | Concurrent branch completion | Apply the join once and preserve both causes |
| T13 | Cancel during execution | Stop new work; record late results without advancing |
| T14 | DST, holiday, timezone, and rule changes | Recompute notification time under declared rules |
| T15 | Device token changes or is reassigned | Old binding cannot disclose another identity's messages |
| T16 | Duplicate and simultaneous wake observations | Suppress one utterance's duplicates; retain separate conversations |
| T17 | Person changes music during speech | Do not resume obsolete playback state |
| T18 | Stale mirror revision or lost cursor | Reject stale update or recover through snapshot and cursor |
| T19 | Private content in index or summary | Unauthorized retrieval reveals neither content nor protected metadata |
| T20 | Unknown schema or unsupported workflow construct | Explicit rejection before execution |
| T21 | Backup restore after external effect | Reconcile receipts before any repeat effect |
| T22 | Child tasks consume one budget concurrently | Total accepted consumption stays within the reservation |
| T23 | Same signed delivery addressed to a different service | Recipient binding check rejects it |
| T24 | Invalid or stale key evidence | Apply defined verification and freshness rules |
| T25 | Exact host outside Kubernetes; native task and sandbox with a host binding | Use only the admitted host and mechanism; enforce required access limits; cleanup preserves bound host data |
| T26 | Coordinator failure, repeated command, and stale control generation | Recover the command receipt; reject conflicting input and stale control; no unexamined repeat effect |
| T27 | Human hold during task completion, disconnect, restart, and cleanup race | Retain the environment until explicit release or recorded forced termination; reject attachment after cleanup starts |
| T28 | Human and agent work together; plan changes during a command | Preserve both authors, plan revisions, and command outcomes; reject stale control and unapproved access changes |
| T29 | Registered session credentials appear in context, output, and errors | Authorized active use works; archive, spool, snapshots, indexes, and observer streams contain references, not values |
| T30 | Short, overlapping, escaped, encoded, or chunk-split secret values | Deterministic substitution under the declared encoding contract; no partial value publication at chunk boundaries |
| T31 | Equal values from two bindings; literal reference text from a tool | Preserve provenance or explicit ambiguity; literal text never triggers secret resolution |
| T32 | Rotate, expire, or revoke a recorded credential without retaining its old value | Playback still works; a new run resolves current credentials under current policy and records new bindings; no historical-value lookup is required |
| T33 | Upload outage and task lease closure before evidence acknowledgement | Recover retained output through limited upload authority; expose gaps; no renewed execution rights or silent batch loss |
| T34 | Multiple agents, branches, handoffs, summaries, and separate task sessions | Recover each call's actual reference-form context and tool results; preserve session, source, and participation links |
| T35 | Task reader lacks access to another agent's context or secret authority | No protected payload or metadata disclosure; task visibility does not grant secret resolution |
| T36 | Bootstrap stable persona and host identities; renew and recover task credentials | No temporary principal is needed; domain administration credentials stay outside workers; supported protocols and current policy govern use |
| T37 | Signed input requires secret substitution | Verify before transformation; distinguish the derived record from independently verifiable original signed evidence |
| T38 | Opaque artifact contains a known secret; storage fills during capture | Store a safe representation or a declared gap; no known secret publication or false completeness claim |
| T39 | Native process continues after coordinator loss; external outcome is uncertain | Enforce admitted local deadlines; reconcile the effect before retry; process termination is not treated as rollback |
| T40 | Join during active agent work and a running command | Both continue; separate pause, resume, and cancellation requests affect only their stated targets |
| T41 | Human changes a file, then optionally messages the initiating persona | No automatic watcher context is injected; the persona can select an explicit message for the worker |
| T42 | Failure in a code task, service request, or media task | Magi records investigation checks and verified assumptions; retention and human involvement follow task evidence, authority, and limits |
| T43 | Revise a persona, then replace it; start several workers; submit a false worker ID | Revisions preserve its identity; replacement uses a different identity; workers use the responsible persona; internal attribution matches admission records, not caller claims alone |
| T44 | Two users share an Executive Assistant persona | Each private or group source request uses the represented user's authority; no combined permission set |
| T45 | Calendar questions followed by biology questions in one conversation | Separate sessions and context; child work stays linked to its task session; earlier data enters only through permitted retrieval |
| T46 | A public session or child step reads an authorized private journal | The session and further analysis inherit source restrictions; calendar-step views and caches do not retain public access; current restrictions apply without rewriting signed payloads |
| T47 | Persona privacy defaults, permitted scope expansion, and private access in a public setting | Scope can broaden when permitted, but a private source wins regardless of interface or physical location |
| T48 | Household policy editors and private owners disagree on processor trust | Enforce each control area; household edit rights do not grant another user's private policy rights; receipt and processing checks remain separate |
| T49 | Administrator and ordinary reader inspect private evidence; evaluator selects an untrusted processor | Administrator can inspect retained evidence without user representation; ordinary access stays scoped; processing policy still rejects the untrusted destination |
| T50 | Evaluate private, group, and public learning candidates from one restricted session | Preserve classification evidence; uncertain or private details remain restricted; permitted general learnings do not expose private source links or reclassify the raw session |
| T51 | Worker proposes an operation with its own justification and claimed scope change | Magi composes a separate evaluation context from authoritative scope and graph evidence; a worker claim does not expand authority; decision binds the examined operation |
| T52 | Inject an SSH key and JWT directly into admitted tools | Tools can use the credentials without a mandatory proxy; archive stores references; broad permissions are not reported as narrowed by model review; cleanup preserves shared source credentials |
| T53 | Query documents, records, media references, and live sources through separate scoped indexes | Magi combines permitted evidence and relationship metadata without one universal graph; excluded nodes do not leak through links or results |
| T54 | Query as a represented user, then as a system for a household, company, or domain | Both paths use actual caller authority and scope; no persona or invented represented human is required; an unauthorized scope is denied |
| T55 | Record a casual preference statement with contrary calendar evidence and missing seasonal context | Retain the communication, context, uncertainty, and contrary evidence; do not create an authored permanent preference or invent mood |
| T56 | Compare user-authored knowledge, accepted subject authority, and an unaccepted claim about a spouse | Apply authority in the adopting context; publication does not impose a belief on another user; higher weight does not erase contrary evidence |
| T57 | Author publishes directly, trusts a persona, or requests review | Honor the author's chosen process and record actual authorship and review; do not impose one knowledge review process or weaken allowance approval |
| T58 | Enrich context from a permitted live calendar and a readable private journal | Apply source purpose and explicit settings; journal read access alone does not admit enrichment; record calendar observation time and missing context |
| T59 | Users accept different beliefs about Alex; deterministic system records conflict | Keep acceptance contexts separate; Alex need not approve another user's private belief; system knowledge can be accepted directly; conflict need not force a global winner |
| T60 | Owner, authorized group or organizational actor, and unrelated actor request invalidation or deletion | Permit operations only within resource authority; an unrelated opinion cannot change the document; deletion is supported without prescribing one retention or dangling-reference policy |
| T61 | Compare adult-child support over time with shared purchases, duplicate bank and Venmo records, and missing sources | Preserve permitted family context and purchase evidence; avoid duplicate totals; return a qualified range when useful; disclose missing coverage and defer inconvenient MFA |
| T62 | Compare daily campaign spending and conversion metrics | Retain definitions, periods, source coverage, and reproducible calculations; expose missing or incompatible data rather than claiming exactness |
| T63 | Inspect a camera alert with recorded suppression, delay, failure, or missing processing evidence | Recover the actual available inputs, policy, assessment, and disposition; distinguish historical records from later analysis and unknown outcomes |
| T64 | Explain a calendar delegation exchange from versioned documents, implementation evidence, and example messages | Distinguish intended from implemented behavior and examples from observed exchanges; identify the supporting sources without claiming new LinkKeys conformance |
| T65 | Interleave three task conversations and reply to an earlier voice prompt | Route the response to the relevant context, preserve pending work, and restore the new topic cue when needed; do not merge sessions |
| T66 | Paste a reply to a voice prompt while answering an unrelated text conversation | Use the declared user settings and context to select destinations and presentation; text and voice can proceed independently; no fixed modality-to-session mapping |
| T67 | One response answers two sessions, including a shared-meaning phrase and private material | Retain source, interpretation, permitted destination inputs, and separate receipts; preserve joint meaning without disclosing unrelated context; delivery retries do not advance a session twice |
| T68 | Record a candidate multi-answer mode for one user and situation, then observe contrary evidence | Keep scope, evidence, uncertainty, and optional weights; do not turn a hypothesis into a universal setting or another user's accepted knowledge; no automatic learning engine is required |
| T69 | Add a corrected interpretation and a new optional assessment dimension to retained interaction layers | Reassemble original inputs and actual deliveries; retain later assessment provenance; do not repeat effects, rewrite history, bypass current policy, or conceal missing and deleted sources |
| T70 | Recall a topic from three weeks ago inside an interleaved interaction | Recover the permitted segment and context; measure against the few-second response and roughly twenty-second continued-retrieval targets under a declared workload |
| T71 | Recall store fails at 03:00 while an alert is deferred, failed, or unrecorded | Report the blocking failure during the active request; distinguish unavailable data from no match where permitted; explain alerts only from evidence and keep diagnosis independent of the failed store |

T25 through T39 were introduced on 2026-10-03. The 2026-10-05 interview revises
T32, T34, and T36 and adds T40 through T52. These are acceptance requirements,
not implemented or measured tests. Use synthetic credentials in all such tests.
Test task and history behavior through APIs before adding a history UI.

T53 through T71 were added on 2026-10-06. They are unimplemented acceptance
requirements. Use synthetic sources, financial records, identities, and media.
Define T70's dataset size, concurrency, exact timing thresholds, and measurement
points before reporting a latency pass. Measure initial response separately
from completed recall. No latency or adaptation result is claimed here.
The [record and storage proposal](knowledge-storage.md) supplies a candidate
workload, timing thresholds, and fixture cases. These remain experiment inputs,
not measured results or approved service guarantees.

Use an injected clock for deadlines and schedules. Use fault injection at
transaction boundaries. Test generated codecs with shared byte fixtures.
Run real local services for integration; use provider simulators only at
external boundaries. A green mocked workflow is not sufficient evidence for
Corndogs, LinkKeys, or database recovery.

## Release and recovery gates

Further local results, 2026-10-12: [support checks](evidence-support.md) expose
small-model quality failures. The [capture prototype](capture-experiment.md)
passes typed-substitution, current-input, and private spool process-recovery
checks. The full suite records 237 leaf passes across three runs. These are
partial W03/W10/W13 results. They do not complete the capture or query API gates.

Begin with repeatable local tests. Then run two-domain integration tests in CI.
Deploy the same immutable artifacts to the persistent lab. Add user-driven
cross-domain checks after those tests pass.

Before unattended lab work, set numeric limits for CPU, memory, disk, model
spend, retry age, and recovery time. Record measured outcomes. Do not invent
capacity claims before measurement. Test process failure, host failure where
supported, and restoration of the documented backup set.

## Next Actions

Review the execution, knowledge, and interaction fixtures for T25 through T71.
Complete local identity, protocol, and recovery gates before deployment. Inspect the existing
example services and prepare the independent partner.local system before
the persistent tests. Do not connect production secrets to these fixtures.
