# Architecture

Status: proposed implementation of the accepted boundaries.

## Component ownership

| Component | Owns | Does not own |
| --- | --- | --- |
| LinkKeys | Identity authentication, key attestations, signed attributes | Permission to edit a calendar, document, or task |
| Registry | Service descriptions, capabilities, endpoint references | Resource data or permission to use an advertised capability |
| Workflow service and interpreter | Definitions, revisions, run records, transition intent, execution rules | Project ownership, computer execution, or LinkKeys policy |
| Corndogs | Current task state and task claims | Workflow language or task code |
| Event service | Publications, subscriptions, delivery obligations, and receipts | Producer transactions, consumer effects, or personal notification choices |
| Local delivery helpers | Optional inbox and outbox records in an application's own transaction | A required client storage engine or authority over another database |
| Notification module | Personal inbox, delivery choices, endpoint bindings | Provider identity as a substitute for LinkKeys identity |
| Agent runtime | Session context, model calls, proposed tool operations under a persona identity | Its own independent approval or final resource authorization |
| Magi knowledge module | Source selection, scoped acceptance, queries, document revisions, relationship metadata, and authorized deletion | A mandatory single graph or permission to override foreign resource policy |
| Longhouse | Projects, human assignments, and their business completion | Workflow definitions or workflow execution rules |
| Execution coordinator | Environment admission, host selection, control assignments, lifetime decisions | Inference hosting or direct resource authorization |
| Host executor | Approved local processes, containers, or VMs; command supervision and cleanup | Global workflow state or domain administration |
| Identity and persona module | Stable persona identities, voice and personality revisions, focuses, subsystem assignments | Temporary task identities or permission granted by a display name |
| Session and policy module | Task boundaries, child links, privacy inheritance, scoped trust revisions | One user's control over another user's private policies |
| Interaction coordination | Pending prompts, channel choices, multi-session reply interpretation, presentation and correction records | One global active conversation or automatic sharing of task context |
| Evaluation module | Context selected across evidence stores, tool-use review, learning classification, later assessments | Unrestricted processing or repetition of effects merely because evidence is readable |
| Secret service | Protected values, issuance, resolution, renewal, and withdrawal | General disclosure through task history |
| Evidence archive | Captured interactions, output, artifacts, context, and causal links | Automatic promotion to knowledge or training data |
| Reactorcide | Existing CI/CD and source components selected for reuse | A required coordinator for Magi tasks |
| Audit module | Required evidence, causal links, administrator evaluation access | Authority to publish private records or bypass processing policy |
| TallyOwl | Operational telemetry and query views | The sole mandatory record of a protected action |

These components have independent API and authorization boundaries. An external
system can use an allowed component without Magi sessions, personas, or workflows.
Keep component CSIL files separate and provide an aggregate client schema.
Components can share a process where useful, but must not require a shared
database, deployment, or Magi-only caller. The [event service](event-service.md)
is the first separate executable with a durable store and protected API.
Magi must support multiple coordinator replicas. A one-process prototype must
not make process memory the authority for command ownership or human holds.

Magi owns knowledge arbitration even when records reside in different stores.
Tools query Magi under an authenticated actor and scope. Representation and
persona use are optional. A storage backend does not become an independent
authority over Magi's interpretation or override a source's access policy.

## Storage

Proposed initial storage:

- PostgreSQL: registry records, definitions, execution links, transition
  intents, inbox/outbox records, notification choices, and audit metadata.
  Also store environment assignments, human holds, command receipts, agent
  participation, context references, and secret binding metadata. Add persona
  revisions, session boundaries, source restrictions, trust policy, and evaluation
  records. Add source, interpretation, presentation, and session-input links.
  Long-term knowledge representations and storage choices remain open.
- Corndogs: current task states and claims, using a supported durable backend.
- Object storage or a local content store: large artifacts, audio, context
  snapshots, and document bodies. Replace secret values before archival storage.
  Address the stored form by digest. Do not publish a digest of a secret value.
- A separate protected store: secret values, bootstrap credentials, and private
  keys. Evidence records contain references to this store, not its values.
  Historical secret-value retention for replay is outside scope.
- Rebuildable indexes: text search, document links, vectors, and audit views.

Use relationship metadata across source-specific representations. A scoped
index can exclude nodes for access or relevance; a universal corpus with only
final-result filtering is not required. Some queries need live source access.
Original evidence and later interpretations need not use the same storage type.
Additional assessment layers must not require rewriting earlier records.
The [record and storage proposal](knowledge-storage.md) refines this design with
admission and delivery boundaries, scoped indexes, cross-domain source adapters,
and an initial retrieval experiment. Its database and search recommendations
remain proposals, not measured implementation results.

An Magi execution link identifies its Corndogs task. It does not expose a
second independently editable task status. Cached status is a projection with
an observation time. Transition recovery across these stores is an explicit
implementation gate; see [Workflows](workflows.md).

An object upload does not itself commit a document revision. Commit the
metadata reference only after the immutable object is durable. A cleanup job
can remove unreferenced objects after a retention interval.

## Domain boundaries

Each domain has its own service identities, keys, access rules, data, and
delivery records. A request across domains must pass both outbound policy and
destination admission. The destination authorizes the resource operation.

Identity domain, service authority, endpoint host, household, and organization
are different fields. Two households can share a host without sharing data.
An identity from a third domain can act on a resource when its owner permits it.

A federation peer is an identity and service instance. A URL is its current
address. Discovering an endpoint does not grant it trust. Verify endpoint
changes against the established identity and local policy.

Use outbound connections or an authenticated relay where a home cannot accept
inbound connections. A relay transfers signed deliveries and receipts. It
does not acquire authority to alter their protected content.

## Registry and placement

Keep stable descriptions separate from expiring observations.

| Stable description | Expiring observation |
| --- | --- |
| Service identity and owner | Last successful authentication |
| API and schema revisions | Supported revision confirmed by probe |
| Platform and installed tools | Available capacity and queue delay |
| Model digest and supported operations | Loaded model and measured latency |
| Data handling and execution policy | Current health and lease expiry |

A placement request specifies required capability, data access constraints,
platform, region when relevant, and resource budget. It can require an exact
host, an execution mechanism, and named directory or device bindings.
A candidate host can refuse it. Recheck availability and authorization at admission.
Reject unsupported combinations. Do not move an exact-host request to another
host or replace a required sandbox with native execution. Kubernetes is one
possible backend, not the placement model.

An inference host can observe the data sent to it. Treat inference placement
as data disclosure. Do not send a private prompt to a different provider merely
because the selected provider is unavailable.
Evaluate processing trust separately from permission to receive a result.
Humans edit trust only within their authorized control areas. An administrator
can read retained Magi evidence for evaluation without representing each user,
but source processing policies still constrain its selected model or host.

## Availability and scaling

Use service-local transactions and per-resource revisions. Do not require a
global transaction or global event order. Allow disconnected domains to keep
working within their own rules. A service can refuse an operation when it
cannot obtain required fresh evidence.

Multiple workers require leases and rejection of stale attempts. Multiple
interpreters require atomic transition admission. Multiple delivery workers
require per-delivery claims. Replica count alone does not provide these rules.
Environment assignments, control revisions, command receipts, and human holds
must survive a coordinator restart. Executors reject commands from stale
assignments. A client can reconnect through another coordinator replica.
See [Execution environments](execution-environments.md) for command recovery
and the limits of cancellation after an external effect.

A lost registry connection must not stop an already admitted job unless its
authorization requires continuous verification. A new operation can use cached
discovery only within its stated freshness limit.

## Operations

Expose liveness, readiness, queue age, retry count, oldest unpublished record,
timeout-sweep age, and storage capacity. Readiness must reflect the functions
a process actually provides. Avoid identity IDs in metric labels.

Restore metadata, task records, and artifacts from a documented recovery point.
Reconcile accepted operations with external receipts before retrying effects.
Refresh withdrawal and key status before protected execution resumes. Test
backup recovery separately from process restart.

Use the Reactorcide secret notation with Magi-owned secret bindings and access
checks. Preserve references in evidence; see [Evidence archive](evidence-archive.md).
Bootstrap credentials must not become ordinary task credentials.
Provide a manual recovery path when Magi
itself cannot start. Agents must not depend on Magi to repair every Magi fault.

Test historical recall and diagnosis when the conversation store is unavailable.
Quiet-hour suppression of unsolicited alerts must not conceal a failure during
an active request. Retain enough independent operational evidence to explain
what is known, without inventing the failed store's contents or alert history.
Recall response targets and the remaining open choices are in
[Knowledge context and reinterpretation](knowledge-and-context.md).

## Next Actions

Connect the independent workflow service to an admitted host executor.
Test coordinator failover, human holds, and late evidence upload. Add Longhouse
only for scenarios that need its project or assignment records.
