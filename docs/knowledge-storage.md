# Knowledge records and storage proposal

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

Proposed 2026-10-09. This design implements the intent of the approved
[knowledge requirements](knowledge-and-context.md). It does not select a
production dependency or complete W03, W10, W11, or W13.

Approved as a starting point on 2026-10-11. The
[first synthetic experiment](knowledge-experiment.md) compiles minimum CSIL
types and tests PostgreSQL storage. Its correctness checks pass. Text search
misses the paraphrase cases and does not meet the proposed recall target.
The [2026-10-12 comparison](retrieval-comparison.md) tests six retrieval paths
on a separate small corpus. It supports further tests of combined search with
explicit evidence links, not selection of a production vector service.

Start with PostgreSQL for Magi records and links, a payload store for large
content, and source adapters for live data. Test text search before adding
vector search. Do not require a graph database, a global corpus, or a separate
knowledge service. Keep each domain independent.

## Minimum records

The 2026-10-16 clarification makes the conversation graph primary. Existing
source, interpretation, session-input, and presentation records form that graph.
Add segment selections of node references or source ranges and versioned analysis
annotations. Selections can overlap; labels such as planning or discussion are
optional analysis data, not conversation types. This adds no requirement for
one graph database. Knowledge assessments can derive from conversation records
and non-conversation sources under the same access and acceptance rules.

Reuse the types in [Native contracts](contracts.md). These are logical records,
not a requirement for one SQL table or service per type. Documents, calendar
observations, media, and interactions keep their own representations.

Use a common evidence envelope where applicable:

| Field group | Proposed rule |
| --- | --- |
| Record reference | Use the existing CauseRef with authority, kind, and stable ID. A revision identifies retained content, not the current state of every related record. |
| Producer | Record the authenticated actor and verified internal agent or attempt IDs where applicable. Keep a reported speaker identity separate from an authenticated identity. |
| Scope | Record the owning control area, source restrictions, and applicable policy references. Persona and represented identity are optional. |
| Order | Record receipt time, available source time, producer stream and sequence, and causal references. Receipt time is not event time or a global causal order. |
| Content | Use SchemaRef with typed content or ArtifactRef. Record capture gaps and transformations. Apply secret substitution before durable storage. |

The first records need the following fields in addition to that envelope.
Optional fields are absent when the source does not provide them.

| Record | Initial fields |
| --- | --- |
| SourceContext | ResourceRef; available RevisionRef; source kind and adapter; observed time; applicable period if known; authorship evidence if present; source-use and processing rules; retained observation or capture gap |
| InteractionRecord | Interaction point; channel; input payload; conversation links; optional client reply hints; available identity and situation evidence |
| InterpretationRecord | Input references; context snapshot used; candidate prompt and session associations; proposed content for each destination; method and evaluator references; uncertainty; optional prior interpretation |
| SessionInput | Destination session; admitted interpretation and source references; actual admitted content in reference form; admission policy and command receipt; delivery and context-use links |
| PresentationRecord | Output or prompt reference; interaction point; reported event such as queued, started, interrupted, or completed; reporter and observation sequence |
| LearningAssessment | Input revisions; assessor and method; scope and permitted audience; supporting and contrary evidence; optional prior assessment and typed dimensions |
| KnowledgeAcceptance | Adopting context; accepting actor; target revision or author and subject area; acceptance evidence; optional replacement or withdrawal reference |
| KnowledgeQuery and QueryResult | Request authority, purpose, periods, precision and effort; retained source selections, answer, calculations, coverage, uncertainty, and retrieval outcome |

Keep authorship with the source revision. Keep acceptance in the context that
adopts it. Neither field grants access. A later document revision does not
inherit approval of exact earlier text unless the acceptance rule permits it.
Accepting an author for a subject is a different rule from accepting one revision.

Reuse ContextSnapshot to record the ordered inputs for each model call or
evaluation. Reuse command receipts and delivery records. An admitted SessionInput
is not proof that an agent received or used it. Record those facts separately.
A query result must retain the actual calculation inputs where permitted;
a link to a changing bank or calendar endpoint is not historical evidence.

Relations use typed roles such as source, interpretation, destination input,
presentation, correction, or use. These roles describe evidence, not a hierarchy
of truth. Multiple sources and destinations are permitted. A shared answer can
have joint meaning that cannot be represented by separate text ranges.

Use SchemaRef for optional extensions. An unknown assessment dimension can
remain opaque in storage. It must not become an implicit permission or policy
input. Reject an operation when a required meaning is unsupported. Keep wire
values stable and test schema changes under the existing CSIL evolution rules.

## Initial API behavior

These operations refine the existing inventory. ReadQuery is a proposed addition
for reconnecting to a lookup. No general client API may insert trusted evidence,
authorize a route, or declare a tool result solely from caller-supplied fields.

| Operation | Request and result | Limit or admission rule |
| --- | --- | --- |
| Interaction.SubmitInput | Input, point, channel, source sequence, optional reply hints; returns an interaction reference and durable receipt | Authenticate the producer. Validate claimed context. Acceptance does not mean delivery to any task. |
| Interaction.ReadPending | Permitted conversations or sessions and a page cursor; returns prompt references and presentation observations | Return a bounded view. Do not expose unrelated prompts or infer attention from playback. |
| Interaction.ReportPresentation | Output reference, point, observation, and sequence; returns an attributed record | Accept only reports for that endpoint's admitted output. The report is evidence, not proof of understanding. |
| Interaction.CorrectAssociation | Earlier interpretation, correction evidence, and expected routing revision; returns a new interpretation reference | Reject a stale revision or return it for renewed review. Do not resend inputs, cancel work, or undo effects. |
| Session.SubmitInput | Admitted interpretation, destination, proposed content, and command key; returns the admitted input and delivery receipt | Check destination authority, source-use rules, and current restrictions. Client submission cannot bypass Magi routing admission. |
| Evidence.ReadInteractionHistory and ReadContext | Interaction or context reference, page cursor, and bounded relation expansion; returns permitted records and known gaps | Check every record, payload, and disclosed edge. Never resolve secret values. |
| Evaluation.AssessLearning | Source references, purpose, and permitted method; returns an assessment reference or work receipt | Magi composes the evaluation context. This operation cannot publish personal knowledge or execute a revised action by itself. |
| Knowledge.Query and ReadQuery | Query scope, purpose, input, effort limits, and request key; returns a query reference, progress, or result | ReadQuery resumes by reference. Authenticate every read and filter results under current policy. Progress can also use resumable events. |
| Knowledge.Explain | Prior result or decision reference; returns recorded sources, calculations, policies, outcomes, and gaps | Keep historical explanation separate from a new assessment. Expose only information the caller can read. |
| Knowledge.AcceptForContext | Adopting context, target, authority evidence, and expected acceptance revision; returns a new acceptance record | Check authority in that context. Do not change another person's acceptance or source access. |

Document edits still use ReadRevision, ProposeChange, CommitChange, and ReadChanges.
CommitChange requires the expected origin revision. A mirror forwards an edit
request to the origin; it does not commit a local replacement. Invalidate and
Delete remain separate authorized operations. Their lifecycle rules need tests
before these operations accept real private data.

All mutations use the command rules in [Native contracts](contracts.md): bind
the key to the authenticated actor, operation, and input digest. A retry with
different input conflicts. Set request, payload, page, and traversal limits
before exposing an endpoint. Reject oversized requests; do not silently truncate
evidence. Bind cursors to the authorized query, not to an unrestricted SQL offset.

Query results distinguish complete within declared coverage, partial, no match,
unavailable, denied, and cancelled. Only reveal these distinctions where allowed.
For example, a caller must not learn that another person's private journal exists
through a denied-source list. A complete search is not a claim of complete world
knowledge. Include source observation times and omissions that can be disclosed.

Disconnecting does not silently restart a query or its tool work. Retain its
request and result references. Use the existing task controls if the lookup
needs asynchronous work. Corndogs remains the task-state authority. Query
progress is not another editable copy of Corndogs task status.

## Shared reply and correction example

This synthetic example checks the record model. It is not a routing policy or
permission to book an appointment. Both initial sessions are private to the
same represented user. The calendar and engineering personas can differ.

| Step | Event | Retained evidence |
| --- | --- | --- |
| 1 | Janaya asks aloud whether Tuesday suits a dentist call in session C. An engineering persona asks in text about a Tuesday release-review call in session E. | Separate prompt and presentation records; both sessions remain active. |
| 2 | The user types, "Tuesday works for both. Put the call after lunch." | One InteractionRecord I1 with its original content and available context. |
| 3 | The test supplies a mistaken interpretation R1: Tuesday for both, but the afternoon constraint applies to C. | R1 links both prompts and I1. It records the context and uncertainty, not an asserted universal meaning of "the call". |
| 4 | The test admits R1 under its fixture policy. | Input C1 gives C Tuesday after lunch. Input E1 gives E Tuesday. Each links I1 and R1 and has separate delivery and use evidence. |
| 5 | The user says, "By the call, I meant the release review, not the dentist." | New input I2 and interpretation R2 reference the earlier evidence. R2 does not replace C1, E1, or R1. |
| 6 | Magi investigates what each session did and decides what correction is needed. | Read pending work, admitted commands, and actual outcomes. An unknown outcome stays unknown until investigated. |
| 7 | Magi admits any needed correction under current policy. | New session inputs and, if needed, new commands refer to R2. The earlier effects and receipts remain historical facts. |

The test deliberately supplies a wrong interpretation. In normal operation,
Magi can clarify the ambiguous phrase before action. The choice depends on the
task and policy. This design does not prescribe a universal confidence score.

If the user later asks what happened, history shows I1, R1, C1, E1, their use,
and the later correction. It does not present R2 as what the agents originally
received. If an external appointment changed, a revised interpretation does not
reverse that change. Repair needs a separately admitted resource operation.

Add a privacy variant: I1 also contains a private journal detail, while E is
group-scoped. Restrict the mixed source and its interpretation accordingly.
Do not mark a selected sentence public merely because it looks harmless.
Magi must either admit it under the required restrictions, withhold it, or obtain
a separate permitted input. A less restrictive derived form needs an authorized
classification decision. Sharing the origin reference cannot expose the private
payload or the existence of an unrelated session.

## Storage comparison

The recommendation below is an engineering judgment for this first experiment.
The linked primary documentation describes available mechanisms, not measured
Magi performance. It was reviewed on 2026-10-09.

| Option | Useful mechanism | Fit for the first implementation |
| --- | --- | --- |
| PostgreSQL plus a payload store | Relational records and links; text matching and ranking; recursive queries for bounded relationship traversal | Recommended baseline. Keep source representations separate and use indexes for discovery. [Text search](https://www.postgresql.org/docs/18/textsearch-intro.html) and [recursive queries](https://www.postgresql.org/docs/18/queries-with.html). |
| PostgreSQL with pgvector | Exact and approximate vector search beside relational filters | Test as an optional retrieval path after the baseline. It is not the authority for knowledge or permissions. [pgvector](https://github.com/pgvector/pgvector#filtering). |
| Metadata store plus Qdrant | Vector retrieval with tenant payload indexes and filters | Candidate when independent vector capacity is useful. Adds a derived store whose updates, deletion, and access checks need coordination. [Qdrant multitenancy](https://qdrant.tech/documentation/manage-data/multitenancy/). |
| Metadata store plus Neo4j | Variable-length path queries | Candidate if measured relationship queries justify a separate graph projection. Do not move every source into it. [Cypher paths](https://neo4j.com/docs/cypher-manual/current/patterns/variable-length-paths/). |
| SQLite for local storage | Local transactional database; WAL permits concurrent reads and a writer | Useful candidate for a device spool or single-host fixture. WAL requires processes on one host and does not work over a network filesystem. It is not the shared file for coordinator replicas. [SQLite WAL](https://sqlite.org/wal.html). |

pgvector's approximate indexes apply filters after the index scan. Selective
filters can reduce result counts. Iterative scans can search further, subject
to limits. A shared approximate index can also let one tenant's vectors affect
another tenant's recall and speed. Test exact search over permitted candidates
first. Evaluate separate tables or partitions where needed; one partition does
not represent all of a person's overlapping group permissions.
[pgvector filtering and multitenancy](https://github.com/pgvector/pgvector#filtering).

These mechanisms do not decide source purpose, delegated access, or accepted
authority. Magi makes those decisions before supplying context to a model.
A vector match is a retrieval candidate, not a verified fact. Failure to find
a lexical match is a reason to test another retrieval path, not to claim that
the original discussion never occurred.

## Proposed placement and write rules

Use relational columns for record IDs, scope references, source revisions,
sequence constraints, and searchable times. Keep typed CSIL content with its
schema descriptor. Do not reduce every payload to untyped JSON or convert
financial quantities to floating point. Preserve exact timestamp values too:
PostgreSQL timestamps have microsecond resolution. Use an exact seconds and
subsecond representation when the source needs more precision; a rounded search
column is not the retained original.
[PostgreSQL date and time types](https://www.postgresql.org/docs/18/datatype-datetime.html).

Store small typed payloads with their metadata where practical. Use a payload
store for audio, documents, large outputs, and snapshots. A local store is
sufficient for a single-host experiment. Replica operation needs a payload store
accessible to all admitted replicas, with tested recovery. This proposal does
not select an object-storage product.

Before committing a large-payload reference, make the stored payload durable
and verify its digest. Then commit the metadata, command receipt, and required
outbox entries in one local database transaction. A crash before that transaction
can leave an unreferenced object, not a successful acceptance receipt. Clean up
such objects only after excluding active uploads and committed references.

Commit each session input and its delivery obligation atomically. Repeated
delivery uses the same input ID; the destination durably records acceptance
before acknowledging it. Mark use only from actual context or execution
evidence. A corrected interpretation has a new ID and needs new admission.
Do not require an atomic transaction across every destination or domain.
Record partial delivery without claiming that all branches advanced.

Maintain a small projection of pending prompts and recent presentation records
for live interaction. Do not search the entire archive before routing each
reply. Rebuild this projection from durable records after a restart. Keep its
revision checks separate from task state and from the user's actual attention.

Publish committed records in a durable order for each local change feed.
Use that publication position as the cursor. A counter allocated before a
transaction commits is not sufficient: a delayed commit must not appear behind
a cursor already returned to a reader. Keep source sequence and event time
separately. Late evidence must appear after the reader's cursor even if its
reported event time is older. A cursor orders one feed, not all domains or effects.

## Access and index boundaries

Store current effective restrictions separately from retained historical
payloads. Admission of a private source must restrict the session before further
context delivery. Read, index, export, and model-processing paths must check
current restrictions even while an index update is pending. Reject or suspend
an operation when required current policy cannot be obtained.

PostgreSQL row security is an additional database control, not Magi's full
authorization model. Superusers and BYPASSRLS roles bypass it; owners normally
do too. Use a separate non-owner runtime role and test restrictive policies.
Foreign-key and uniqueness checks can disclose information through errors even
when rows are hidden. Map those errors to safe API results.
[PostgreSQL row security](https://www.postgresql.org/docs/18/ddl-rowsecurity.html).

Authorize candidate selection before snippets, reranking, model calls, or
relationship expansion. Check processing trust for the embedding model as well
as the answer model. The permitted audience is not one ordered label such as
public, group, private: unrelated groups and private owners have distinct rules.
Apply the intersection of restrictions from the sources actually used.

Every derived entry needs its source revisions, applicable restriction references,
index generation, and derivation method. This includes embeddings, summaries,
document links, and query caches. Prefer separate scoped projections where
required; do not require all private data in one search collection. A query
cache must include authority, purpose, source versions, and relevant policy
versions, not only the question text. Recheck access before returning it.

Bound relationship traversal by permitted relation kinds, depth, record count,
and time. Detect cycles. Apply access checks to edges and endpoints. A visible
record does not make every linked record visible. Do not disclose hidden titles,
counts, or source identities through an explanation.

Before a deletion API is enabled, define removal from payload storage, indexes,
caches, retained derivatives, and backups. Restrict access when deletion is
accepted, then record physical cleanup progress separately. Do not report full
erasure from the first metadata change. Authorized deletion can remove evidence;
history must show only permitted gaps, not a retained copy under another name.
The detailed retention and propagation choices remain O18.

## Domain and source adapters

Each Magi installation owns its metadata and processing policy. A source adapter
describes the representations it can supply, its origin, change cursor, revision
rules, and live-query support. Magi verifies these claims before use. A source
without stable revisions can supply a retained observation with a local digest
and observation time; do not invent an authoritative source revision.

A Markdown source can expose editable files as well as its API. Capture a
revision at the origin when an authorized edit is accepted. Keep a stable
document ID across path changes. A file timestamp alone does not prove human
authorship. Mirror-side drafts are not committed revisions. Recording a file
change does not require a file-watcher message in an agent's context.

For a mirror, retain the origin identity, origin revision, local receipt cursor,
and access freshness evidence. Duplicate changes are harmless. A feed gap needs
an authorized resynchronization. Index the new committed revision, not an
unconfirmed local edit. Loss of contact is not permanent permission to serve a
cached foreign record. Apply the source agreement and the operation's freshness
policy. Receiving permission and processing permission remain separate.

Test with independent catalystsquad.local and partner.local authorities. Use their
disposable equivalents first. A valid foreign identity is not a trusted source
or permission to query another domain. Foreign keys are database mechanisms;
use authority-qualified references for remote records. Neither store needs a
live cross-database join to interpret those references.

## Retrieval paths

Select the path by the question, not by one mandatory retrieval algorithm.

| Scenario | First retrieval path | Evidence needed in the answer |
| --- | --- | --- |
| Adult-child spending comparison | Permitted family knowledge plus typed bank, payment, and calendar observations | Periods, shared-cost and transfer reconciliation, included records, range assumptions, missing sources, and questions deferred |
| Campaign performance | Structured source queries and exact calculations | Metric definitions, currencies, timezones, denominators, attribution windows, missing periods, and reproducible inputs |
| Camera non-action | Event and decision references, then bounded causal traversal | Original input, policy, recorded assessment, actual suppression or failure, and unknown outcomes |
| Delegation explanation | Versioned authored documents, code evidence, and message examples | Accepted subject authority, relevant versions, and clear separation of examples from observed exchanges |
| Three-week conversation recall | Time and scope filters, text search, then optional vector candidates and context links | Relevant source segment, interpretation and actual session input, neighboring permitted context, and capture gaps |

For recall, retrieve candidates from permitted projections, obtain the relevant
source revisions, and assemble a bounded context. Do not return a whole mixed
conversation by default. If the source is unavailable, a retained permitted
summary can still help, but label it as a summary. Do not call it recovered
original evidence. A live calendar answer must report its observation time.

## First experiment and acceptance checks

Use synthetic data before real private records. Compare the PostgreSQL text
baseline with the same records plus optional pgvector retrieval. Defer Qdrant
and Neo4j experiments unless a measured limitation justifies them. Pin dependency
versions, model revisions, and the corpus seed when building the experiment.

Proposed workload: two independent authorities; 100,000 text segments per
authority across 10,000 task sessions; overlapping private, household, and work
scopes; ten concurrent queries. Include at least 100 labelled recall questions,
paraphrases without matching keywords, corrections, and the interleaved
three-week-old discussion. Measure both cold and warm caches. Declare hardware,
payload sizes, source delays, and model inference times with the results.

Proposed timing thresholds are a first translation of the user's targets,
not approved service guarantees. For healthy sources, target p95 useful recall
within three seconds. If a lookup needs longer, acknowledge within two seconds
and target p95 completion within twenty seconds. Do not count an acknowledgement
as recall. Report failures and timeouts in the denominator and separately report
the fraction of questions answered correctly with source support.

For this query experiment, measure from receipt of the complete request to the
response at the test client. Include query planning and model time where used.
Report the storage-only time separately. Device tests in W11 must also measure
speech capture, recognition, transport, and presentation; query timing alone
does not prove the full voice response target.

Use labelled source matches for retrieval quality. Propose at least 95 percent
recall of the expected source within the first ten permitted candidates.
Measure answer correctness separately; exact vector neighbors are not proof of
semantic correctness. No unauthorized content may reach a result or an untrusted
processor in the test set. A faster result does not compensate for an access leak.

| Check | Existing requirement coverage |
| --- | --- |
| Shared reply, mistaken interpretation, later correction, and separate context-use records | T65 through T69 |
| Private detail in a mixed reply; stale public index; unauthorized source, edge, count, or cached result | T53, T54, T58, T67, and T69 |
| Authored knowledge versus casual communication; independent acceptance by two users | T55 through T59 |
| Authorized deletion with missing originals and blocked stale derivatives | T60 and T69; detailed lifecycle must be selected before claiming completion |
| Ranges, exact calculations, recorded non-action, and supported protocol explanation | T61 through T64 |
| Crash after object upload, after metadata commit, and after destination acceptance before acknowledgement | T67 and W04/W13 recovery work; no claim of exactly-once external effects |
| Mirror duplication, feed gaps, edit rejection, and foreign access withdrawal | T18, T19, T53, and T54 |
| Concurrent commits, a delayed lower source sequence, and reconnect after publication | W04 delivery recovery and W13 late-evidence capture |
| Recall latency and quality; source or metadata outage at 03:00 | T70 and T71 |

For outage tests, use a known failed source and a source that times out. Return
a truthful failure or partial result within the declared lookup deadline. Keep
health and alert-delivery evidence outside the failed conversation store.
If that independent evidence also fails, report the limit. Do not infer that
quiet hours explain a missing alert. A recovery action needs separate authority.

These are experiment requirements, not a list of completed tests. The
[results](knowledge-experiment.md) define the measured subset. Live federation,
full query latency, and production acceptance remain open.

## Next Actions

1. Extend the [support experiment](evidence-support.md) with a new evaluation
   version. Keep the measured retrieval comparison frozen. Use new questions
   for scale and tuning.
2. Review the compiled experimental records and errors for W03. Keep W02's
   authentication and approval integration gate open.
3. Connect the [local capture prototype](capture-experiment.md) to admitted
   executors. Test network delivery and separate-domain recovery.
4. Resolve deletion and capture limits before storing real private evidence.
   Keep adaptive modes, weight scales, and reflection schedules out of this gate.
