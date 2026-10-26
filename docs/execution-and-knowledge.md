# Execution, knowledge, and audit

Status: proposed boundaries and acceptance requirements.
The accepted execution and evidence decisions were expanded through 2026-10-06.
Detailed contracts are in [Execution environments](execution-environments.md)
and [Evidence archive](evidence-archive.md).
See [Personas sessions and trust](personas-sessions-and-trust.md) for the
identity, session, privacy, and evaluation boundaries.
See [Knowledge context and reinterpretation](knowledge-and-context.md) for
multiple stores, contextual acceptance, query scenarios, and later assessment.

## Agent runtime

The runtime accepts a task, context references, allowed tool descriptions,
model requirements, and resource limits. It records each model request and
response, validates proposed operations, and sends commands to resource
services. The services retain final authorization.

Separate conversation history, task sessions, and execution state. A CLI,
browser, phone, or voice client can serve several conversations. A new task
starts a separate session. Child work remains within the parent session's
privacy boundary. Give each input a stable ID and
define how concurrent inputs are ordered. A disconnected client does not cancel
a task unless the request explicitly specifies that behavior.

Several sessions can remain active across voice and text. One interaction can
answer prompts in several sessions. Keep the source response, interpretation,
and actual destination inputs linked without combining session permissions.
Magi owns real-time routing and presentation; each worker need not receive the
whole interaction history to determine what its own input means.

Workers use the responsible persona's stable LinkKeys identity. Internal task
and agent IDs provide evidence links, not temporary authenticated principals.
Magi evaluates proposed tool use outside the worker context, with relevant
graph evidence, task scope, and policy. Record that evaluation separately.

For each model call, record endpoint identity, model revision when available,
adapter version, generation settings, input artifact digests, and output.
Do not promise deterministic regeneration from a random seed. Historical replay
uses recorded observations; a new inference run is a new execution.
Retain the actual available input and output, not only their digests. Record
non-LLM model interactions too. A short-lived credential can enter an authorized
active session when policy permits it. Persistent context and archive records
must contain its secret reference instead of its value.

Retrieved text and tool output are data. They cannot install policy, change
tool permissions, or approve a deployment. A model can propose these changes
through the same authorized APIs available to other clients.

## Inference contract

Describe supported modalities, streaming, context limits, structured-output
constraints, log probabilities, sampling controls, cancellation, and optional
native model controls. A request marks requirements separately from preferences.
Reject a missing requirement. Record any selected fallback and its reason.

An adapter can call a network API, local process, or host service. Supply
credentials where the admitted tool or adapter needs them. Direct injection
is supported; a universal proxy is not required. Keep long-lived values out
of model context. Probe capabilities with bounded, non-sensitive inputs.

Apply processing-trust policy for the relevant personal, group, or other
control area. A shared persona does not grant one user control of another's
private trust policy. Keep processing permission separate from result delivery.

lo-agent is a reference for capability discovery and optional inference
controls. It is not an Magi dependency or evidence that every provider supports
those controls. See [Evidence](evidence.md).

## Execution placement

Magi owns environment admission, control, and lifetime. Reuse selected
Reactorcide components without requiring its job coordinator. Compose a target
host, execution mechanism, tool definition, bindings, authority, and limits.
Support native execution, containers, and Windows or macOS VMs. A task can
require a specific host outside Kubernetes, or a sandbox with an approved host
directory. Native execution does not provide the same isolation as a sandbox.

Separate an environment from the commands and tasks that use it. A human can
inspect it, enter a shell, change an authorized plan, or work with an agent.
An explicit human hold prevents normal cleanup until release. A disconnected
shell is not a release. Control revisions and command ownership must survive
coordinator failover. See [Execution environments](execution-environments.md).
Joining does not pause agents or cancel commands. Those are separate requested
operations. Users can inform the initiating persona through explicit messages;
that persona selects useful worker context. Do not inject file-watcher events.

After failure, gather evidence and verify assumptions. Magi decides retention,
further investigation, and human involvement per task. Preserve required
resources under the task's limits instead of applying one failure cleanup rule.

Use OCI images and VM bundles by immutable digest. Use pinned Webi or mise
tools when building environments. An image digest identifies an artifact;
it does not prove that its source or build was trusted. Record source revision,
trusted job definition, build result, and artifact digest together.

For foreign work, begin with approved task templates and typed parameters.
Arbitrary code requires an explicit execution class. Keep private data and
public contribution jobs in separate workspaces with separate credentials.
Do not expose host runtime sockets as a routine task capability.

Reserve compute, model-call, time, and spending budgets atomically where shared.
Children consume the parent's reserved budget. A new worker or retry must not
reset the remaining budget. Admission can queue or reject work when capacity
or authority is insufficient.

## Evidence and context history

Capture available model interactions, command inputs and outputs, logs, results,
artifacts, human actions, and context changes before later assessment. Record
capture limits and gaps. Do not silently discard output or claim unavailable
model internals were captured.

The archive uses Reactorcide's `${secret:path:key}` notation. Magi must retain
the binding between each value and its reference when it resolves the secret.
Replace secret values before evidence enters durable archive storage and indexes.
Reading history does not authorize secret resolution or repeat an action.

Task views show the session boundary, participating agents, actual model context,
tool results, branches, handoffs, and human changes. New sessions do not inherit
old context automatically. Context summaries and their source records remain
separate. Replay uses current credentials; historical secret values are outside
scope.

Preserve presentation and routing records with the source evidence. Later
assessments can add hypotheses, context-specific weights, or questions about
intent, mood, or tone. These are extensible dimensions, not initial required
classifiers. Reprocessing must not rewrite earlier inputs or repeat effects.

Evidence retention, knowledge publication, and training-data selection are
different decisions. Assessment can label records for later use or removal.
It does not itself authorize disclosure, training, or deletion. See the
[Evidence archive](evidence-archive.md) for replay and retention rules.

## Knowledge records

Magi arbitrates knowledge across different sources and stores. External systems
query Magi through APIs. A persona or represented human is not required when a
system acts under its own authority for a household, company, or domain.

Documents are one supported representation. A document can have a ResourceRef,
immutable revision, body digest, author, access policy reference, and links.
Markdown supports direct human authorship. It is not the representation of
every graph entry. Sources can also expose records, media, or live queries.
Relationship metadata connects them without a universal graph hierarchy.

Use this write sequence:

1. Read the authoritative revision.
2. Submit a proposed body and expected revision to the authority.
3. Check permission and concurrency at the authority.
4. Commit the new revision and publication record together.
5. Update mirrors and derived indexes from the authoritative change.

An offline proposal remains a proposal until the authority accepts it. A
revision conflict returns both revision references for resolution. A mirror
does not invent a replacement revision. Domain migration needs a signed
authority-transfer process before the old origin is retired.

For direct editor access, provide a working copy with an authenticated submit
command or a controlled filesystem interface. A file watcher cannot alone
prove author identity or permission. Keep file paths separate from document IDs.

Support authorized invalidation and deletion by the owner, an authorized group
role, or applicable organizational authority. Disagreement alone does not permit
changes to another document. Detailed retention and deletion behavior remain open.

## Retrieval and memory

Authorize retrieval before giving content to a model. Apply access checks to
titles, snippets, backlinks, summaries, vectors, and query counts as well as
document bodies. Record the exact revisions used in a context snapshot.

Keep source facts, human assertions, imported claims, and model suggestions
distinguishable. Store source references and correction history. A model's
confidence score is not permission to promote a suggestion to accepted knowledge.

Ordinary conversation is communication, not authorship. Preserve context and
supporting or contrary evidence without declaring a permanent fact. Give greater
weight to human-authored knowledge from the user or their accepted authority
for the item or subject. The author chooses persona assistance and review.

Acceptance of personal knowledge belongs to the adopting context. A user can
accept a private belief about Alex without changing Alex's own accepted view.
System and deterministic knowledge can be accepted directly. Conflicts can
remain unresolved or be interpreted for a specific case.

Separate private, household, organization, and project context. Use caller
authority and scope, plus persona defaults and representation when applicable.
Broaden scope only when content and policy permit it. A private source restricts
the whole session, including other
steps and child work. Ordinary summaries retain source restrictions.

Source-use settings and implied purpose constrain enrichment separately from
read access. Private journals are not routine enrichment sources. Relevant live
calendar context is permitted under its policy. Retain source times and report
coverage gaps. Scoped indexes can omit unauthorized or irrelevant nodes entirely.

Queries can require exploration, calculations, historical inspection, or a
technical explanation. Precision and user effort follow the question's purpose.
Support ranges for incomplete exploratory data and reproducible calculations
for precise metrics. Use the scenarios in the knowledge requirements to test
these differences and historical recall latency.

Later evaluation can classify individual learnings by their actual sources.
Keep uncertain cases restricted. A broader learning must not expose private
examples or protected source links. This does not reclassify the raw session.

Replicated plaintext cannot be made unreadable to a recipient who retained it.
Offer query-only sharing where replication is unsuitable. Cooperative mirrors
must process deletion and permission changes, including derived indexes.
Document the removal limit instead of claiming remote erasure.

## Audit

Record command, actor, represented identity, allowance evidence, policy
revision, input references, effect attempt, result, and causes. A record can
have several causes. Store private payloads separately from operational
metadata and apply access checks during graph traversal.
The Magi administrator can evaluate retained evidence under its own authority,
not on behalf of each user. Its evaluation still follows source privacy and
processing-trust rules. Represented persona access remains user-scoped; system
queries use their own authorized scope where no user is represented.

Commit mandatory local audit metadata with the protected state change. If an
external effect cannot share that transaction, record intent first and retain
its reconciliation status. Do not rely on sampled telemetry for completeness.
TallyOwl can provide operational views and correlation.

A model explanation is an attributed output, not proof of internal reasoning.
Inspection must distinguish recorded non-actions, deferred work, failures, and
missing evidence. Later interpretation is not the original decision rationale.
Signed records prove attribution under their trust assumptions; they do not
prove that an operator retained every record. Define retention and independent
checkpoints if tamper evidence is required.

Keep self-improvement work under ordinary repository and deployment rules.
An agent that proposes a policy change does not approve it by writing the
proposal. Separate source testing, artifact publication, and deployment rights.

## Next Actions

Review the environment and evidence contracts. Test exact-host work, a human
hold, secret substitution, and a task with two agents. Verify that reconnect
shows the same task, context, command results, and capture gaps.
Include interleaved interaction, multi-session replies, and later assessments.
Compare knowledge storage and retrieval designs using the interview scenarios.
