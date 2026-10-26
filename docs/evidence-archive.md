# Evidence archive and task history

Updated 2026-10-06. Status: accepted retention and secret-handling decisions
with proposed storage and query contracts. No archive service is implemented.

Update 2026-10-12: the [capture experiment](capture-experiment.md) implements
typed substitution, current-input preparation, and private local spool recovery.
It uses synthetic data. Authenticated upload and executor integration remain open.

The [question and answer prototype](question-answer.md) now retains bounded
model request and response evidence with a protected read API. It accepts no
model credentials or restricted sources. This is not the general archive,
secret-reference capture, or authenticated stream-upload implementation.

Capture available model interactions, tool operations, logs, and results before
assessing their later value. Store secret references instead of secret values.
Provide task and agent history through APIs. Knowledge admission, context
selection, and training-data assessment are separate later operations.

## Capture scope

Capture all exposed request and response content from text-based models,
including non-LLM models. Retain other available results and input artifacts
where capture, authorization, and media handling permit it. Record unsupported
capture explicitly. Do not claim access to model internals that an API omits.

Keep these records linked:

- Session, task, workflow, step, attempt, agent instance, and environment IDs.
- Canonical actor, represented identity, allowance evidence, and policy revision.
- Stable persona identity, session boundary, source privacy, and processing policy.
- Persona and plan revisions, including human changes and reasons supplied.
- Context before each model call, its source revisions, and selected tool schemas.
- Model endpoint, model revision when available, settings, requests, and responses.
- Tool arguments, input, output, exit status, receipts, and uncertain outcomes.
- Human messages, shell operations, handoffs, environment holds, and releases.
- Files, patches, reports, media, measurements, and export manifests.
- Failure investigations, checked assumptions, evaluation inputs, and decisions.
- Interaction points, channel events, pending prompts, and multi-session reply links.
- Interpretations, actual delivered inputs, corrections, and later assessments.

Record exposed streaming chunks and their sequence when available. Also provide
an assembled view without losing the chunk records. Preserve failed, cancelled,
and superseded attempts. A successful retry does not erase the earlier attempt.

## Linked evidence layers

Keep original interaction evidence, interpretations, and actual session inputs
separate and linked. These are minimum roles, not exactly three fixed layers.
Presentation events, context observations, evaluations, and learning assessments
can add more. Records can reside in different stores with source references.

One reply can address several questions across sessions. Retain its shared
origin and each destination's admitted input. Preserve joint meaning where
needed; splitting by text range alone can lose it. Do not expose sibling
context through a shared reference. Record each delivery and correction.

Retain available media and context under source permissions and capture limits.
Distinguish original media, transcript revisions, and later interpretations.
Do not claim a transcript contains uncaptured tone or that playback proves
human attention. Keep secret substitution and explicit gaps at every layer.

Later review must reconstruct what was available, what was inferred, and what
was actually used at the time. Add new assessments without replacing those
records. Proposed extension records identify schema, assessor, method, input
revisions, scope, and supporting or contrary evidence. Hypothesis weights,
likely intent, and mood or tone questions remain optional dimensions. No fixed
scoring scale or complete learning model is required initially.

Reprocessing is a new assessment, not replay of the original task's effects.
It must not resend a reply or apply a changed routing decision automatically.
Use current access and processing rules. New evidence or human review can add
understanding without batch reprocessing. Deleted or unavailable inputs limit
reconstruction. Do not present a derived summary or index entry as the recovered
original. Apply authorized deletion policy without inventing missing evidence.

See [Knowledge context and reinterpretation](knowledge-and-context.md) for
acceptance, authority, and query requirements.

## Secret reference notation

Use Reactorcide's notation: `${secret:path:key}`. For example, an archived
header can contain `Authorization: Bearer ${secret:services/calendar:token}`.
This is an example reference, not a credential or a configured secret path.

Reuse the pattern of registering resolved values and matching them in output.
Change the replacement to the corresponding secret reference. The current
Reactorcide masker stores only values and emits a redaction marker. Magi needs
to preserve each value's source binding from resolution onward.

Proposed SecretBinding metadata contains a binding ID, secret authority,
reference path and key, and available version or issuance metadata. It also
contains session scope, validity, and resolution policy. The archive stores
no secret value.
The secret service stores values separately under its own access and retention
rules. The same path in two domains does not identify the same secret.

Short-lived credentials may be present in active session context when admitted.
Register generated credentials before exposing them to a tool or model. Give
them session-scoped references, even when they have no retained value after
expiry. An expired value can be unavailable without invalidating its history.

Magi does not retain historical secret values for replay. A version or issuance
ID records what was used; it is not a promise that the value remains available.
Specialized historical-value needs belong outside Magi. A new execution resolves
current credentials and records its bindings without rewriting the old record.

## Substitution boundaries

Apply substitution before durable evidence writes, local spool writes, search
indexing, persistent context snapshots, and general observer streams. Guard
adapter diagnostics and failure paths too. Authorized live context and the
secret store are separate from this reference-only archive boundary.

Use structured secret fields when available. For free text, retain a bounded
matching buffer across chunks so a split value is not published in pieces.
Define rules for UTF-8 boundaries, escaping, and supported encodings. Do not
claim that a literal-value matcher finds arbitrary transformations or unknown
credentials. Interactive secret input needs registration and a protected path.

The implementation must handle short values, overlapping values, equal values
from different references, and rotation during a stream. Prefer field provenance
over guessing from text. Use deterministic matching. If a value maps to several
bindings, record the ambiguity without the value. Do not silently choose a
reference for a new execution.

Keep literal text distinct from substituted secret segments. A tool can print
`${secret:services/calendar:token}` without ever receiving that secret. Such
text must not become a request to resolve it. The proposed typed segment map
records binding IDs, field or byte locations, and supported transformations.
Render those segments with the familiar notation. Do not recursively expand
archive text, tool output, or inserted reference syntax.

Secret reference names can themselves disclose private metadata. Apply access
checks to binding metadata, search results, and graph traversal. A view can
hide restricted metadata without changing the underlying record. Viewing
evidence never grants secret-resolution rights.

For files or media that cannot be safely substituted, use a safe derived form
or record a capture gap. Do not publish known secret values because a format
is opaque. Define limits for each adapter; a scan is not proof that a payload
contains no unknown secrets. Keep ordinary credential headers and private keys
out of general diagnostic output in the first place.

## Archive integrity

A substituted record is not the original byte stream. Store its digest and
substitution metadata. Record the capture component, schema, and transformation
version. Do not claim byte-identical reconstruction of secret-bearing inputs.
Do not publish bare secret-value hashes as binding identifiers.

Verify signed input before changing it. Preserve exact signed bytes when they
contain no secret values. If a payload requires substitution, record an
attributed verification result and a derived archive representation. The
original signature does not authenticate changed bytes. The derived record
must not serve as independently verifiable original authorization evidence.
Keep secret values out of signed allowances and durable control messages.

## Storage and delivery

Proposed storage uses durable metadata and immutable payload objects. Store
context snapshots or lossless deltas that can reconstruct a named snapshot in
reference form. Retain every required base revision. A digest without retained
content or a durable retrieval contract is not a recoverable context record.

Batch streams under producer identity, attempt, stream ID, and sequence. Bind
repeated sequences to the same content. Reject different content at the same
sequence. Use atomic uniqueness or conditional writes, not a read-then-write
assumption. Keep durable publication receipts and export manifests.
Several workers can use one persona identity. Bind each stream to its admitted
internal agent and attempt IDs too. Those IDs support attribution within Magi;
they are not independent LinkKeys principals.
A persona signature alone cannot distinguish its workers. Proposed checks use
coordinator and executor admission records to validate stream attribution.
Caller-supplied worker IDs alone are not sufficient evidence.

Separate execution authority from evidence-upload authority. Closing an
execution lease must not discard its pending evidence. A bounded upload grant
can permit only the admitted producer's remaining records. It cannot authorize
new commands or alter already accepted records.

Record capture as complete, incomplete, or unknown. A final manifest includes
expected stream ends, received ranges, gaps, and artifact references. Completion
means completeness under that declared capture contract, not knowledge of all
internal model or process activity.

Define storage limits and outage behavior before deployment. Retry durable
uploads and apply flow control. Do not silently discard old batches. If capture
cannot continue, pause work where safe or mark the gap. Refuse new protected
effects when their mandatory intent record cannot be made durable. A failed
host can still lose unacknowledged data; report that limit.

Environment cleanup does not delete the archive. Later assessment can exclude
items from training or propose retention changes. Apply authorized deletion
policy separately, including dependent snapshots, indexes, and datasets. Do not
interpret rejection for training as automatic loss of audit evidence.
Immutability while retained is not a ban on authorized deletion. Knowledge
deletion can remove material needed for later reconstruction. Detailed retention
and dangling-reference rules remain open; do not promise indefinite recovery.

## Task and agent views

Provide these API views before requiring any official UI:

| View | Required content |
| --- | --- |
| Task history | Session boundary, attempts, branches, agents, environments, outcomes, and causal links |
| Agent history | Persona identity and revisions, separate session participation, model calls, tool results, and handoffs |
| Context at a call | Ordered messages, instructions, source content, tool schemas, summaries, and secret references actually supplied |
| Context changes | Additions, removals, truncation, compaction, and links from summaries to source records |
| Operation detail | Admitted arguments, input/output streams, retries, receipts, and capture status |
| Human participation | Authenticated messages, commands, plan edits, control changes, holds, and releases |
| Comparison | Selected differences between attempts, plans, contexts, model settings, and results |
| Evaluation | Assembled context, selected graph records, applicable policies, evaluator, and decision |
| Interaction history | Original responses, presented prompts, channel changes, routing interpretations, actual inputs to each session, and corrections |
| Assessment history | Earlier and later interpretations, source and method revisions, context-specific weights when supplied, and reconstruction gaps |

A session can contain many agents and child tasks. A conversation can continue
across sessions, but a different task starts a separate context. Use explicit
participation and context links rather than copying private history between
sessions. Store what each call actually received, not just the conversation as
it appears now. A handoff links the sender and recipient views. Sessions can
interleave across channels. A common input can advance several sessions without
granting access to their combined histories.

Expose both a local sequence and causal references. Wall-clock sorting is a
convenient view, not a global execution order. Preserve parallel branches and
multiple causes. Late records remain queryable after a client resumes its cursor.

Authorize each record, payload, and edge. Permission to see a task does not
automatically expose every agent's other tasks or private context. Use stable
pagination and resumable streams. Keep derived query indexes rebuildable.

The Magi administrator can evaluate all retained Magi evidence under its own
authority. It does not represent each user to do so. Ordinary users and personas
retain their scoped access. Administrator access does not remove source privacy
or processing-trust rules and does not grant access to external source records.

The most restrictive source requirements apply to the session, including child
work and later analysis. Apply the effective restriction to history views,
indexes, and exports. Scope changes must not leave an earlier public view
available after a private source restricts the session. Previously delivered
information cannot be recalled. See [Personas sessions and trust](personas-sessions-and-trust.md).

Proposed enforcement records current access restrictions separately from
immutable payloads. Keep signed bytes unchanged. A label recorded at capture
does not override the current session restriction or resource policy.

## Playback and replay

These are separate operations with separate permissions:

| Mode | Secret handling | Effects |
| --- | --- | --- |
| History playback | Display references; never resolve values | None |
| Input preparation for a new run | Resolve current references under current policy; record new bindings | No tool or model execution |
| New execution from history | Resolve or issue credentials under current policy; record every replacement | New attempt with explicit effect authorization |

The archive is not a credential authority. Resolve current references only
after checking the caller, destination, purpose, and current policy. Report
unavailable or ambiguous references. Historical-value reconstruction is outside
scope. A revoked or expired credential is not revived by an archive reference.

If a replacement credential is issued, record a new binding and its relation
to the old one. This is not byte-identical replay. History playback does not
call model providers. A new model call can differ even with the same settings.
External operations still need receipts and retry rules; input preparation does
not establish that a business action is safe to repeat.

## Knowledge and training assessment

Keep the archive, accepted knowledge, and context construction separate.
An assessment adds provenance, usefulness labels, corrections, or dataset
membership without rewriting the observation. Record assessor identity and
version when automated. Permission to retain data is not permission to train
on it or share a derived dataset.

Evaluation can classify individual learnings as private, group-scoped, or public
based on their actual sources and task scope. Keep uncertain cases restricted.
A permitted general learning can have a broader scope than its source session,
but it must not disclose private details or restricted evidence links. Record
the classification decision. Do not lower the raw session's privacy or treat
ordinary context compaction as learning evaluation.

Build evaluation context from the graph outside worker contexts. Record the
selected evidence, plan and policy revisions, evaluator, model request, and
decision. Use a processor trusted for all applicable source restrictions.

Knowledge records link to evidence and its access policy. Queries can use typed
lookups, graph traversal, text search, summaries, or embeddings. No one method
is mandatory, and the records need not be in one graph or storage type. Record
the selection and distillation steps used for each context. Conversation is
evidence of communication, not automatically authored or accepted knowledge.
Keep scope classification separate from personal-knowledge acceptance.

## Next Actions

Review the compiled experimental SecretBinding and segment schema. Connect
the tested local substitution and spool path to admitted producers, current
resolver authorization, and durable remote upload. Test session
privacy across child work, administrator evaluation, and scoped learnings.
Add shared-response, routing-correction, and later-assessment fixtures. Prove
that reprocessing leaves original inputs and external effects unchanged.
Compare storage designs without requiring a single graph database.
