# Knowledge context and reinterpretation

Requirements approved by the user on 2026-10-09. Use them as the design baseline
and revise them as experience with the system identifies needed changes.
Record mechanisms remain proposed. Storage engines and learning algorithms
remain open. Approval does not mean that implementation or testing is complete.

Magi connects knowledge, live sources, original evidence, and interpretations.
It decides what applies to a query under the caller's authority and context.
The information need not reside in one graph, database, or document format.

Clarified 2026-10-16: the conversation graph is the primary interaction model.
Modes are optional interpretations of overlapping graph segments, not types
assigned to conversations. Knowledge derives from that graph and other evidence
sources, including searches and live systems. Source acceptance rules remain.

## Sources and authority

Sources can expose documents, records, media, observations, or live queries.
Markdown documents are one kind of entry, not a required representation for
every item. Relationship metadata connects relevant records across stores.
Do not require one universal node hierarchy or one shared index of all data.
An index used for one context can omit nodes for access or relevance reasons.

Magi is the system that selects and interprets knowledge. Other systems can
query it through tools and APIs. A separate knowledge service does not replace
Magi's authority. Physical storage and service boundaries remain choices.
Each source still controls access to its records. Each authoritative record
has an origin; authorized changes occur there before mirrors receive them.

A query identifies its authenticated LinkKeys caller, scope, and purpose.
It can name a represented identity when applicable. A system can also act
under its own authority for a household, company, or domain. Neither a persona
nor a represented human is mandatory. Naming a scope does not grant access.

## Communication and authored knowledge

A conversation records communication. It is not automatically an act of
authorship or a declaration of permanent truth. Retain the statement and its
available context: time, place, surrounding exchange, and relevant sources.
Keep supporting and contrary evidence, missing context, and uncertainty.

For example, a statement about preferring morning appointments can be casual
or temporary. Calendar behavior can support or challenge an interpretation;
it does not by itself prove a preference. Separate observed cues from inferred
intent, mood, or tone. Do not claim to know context that was not captured.

Human-authored knowledge has greater weight when the user authored it or
accepted its author as authoritative for that item or subject. Authority is
specific to the adopting context. Publication alone does not give a claim
authority in another person's context.

The author chooses the review process. They can trust a persona to prepare
and publish knowledge, request a review, or inspect each line. Record the
authorship and review that actually occurred. This choice does not change the
separate exact-byte approval requirement for delegation allowances.

## Acceptance and disagreement

Personal knowledge needs acceptance in the context that will use it as such.
A user can accept a belief about Alex for their own private context without
Alex's approval. Alex has a separate acceptance context about Alex. One spouse
cannot make claims authoritative in the other's private context by publishing
them. Acceptance does not grant access to another person's private sources.

Systems and other deterministic knowledge can be accepted directly. Do not
require human approval for every node, relationship, or interpretation. Keep
evidence and tentative interpretations distinguishable from accepted personal
knowledge. Authorship, acceptance, access, and confidence are separate concepts.

Conflicting knowledge can coexist. Context can favor one account or leave the
conflict unresolved. Do not require a hierarchy that ranks all relationships
or forces one globally correct account.

Invalidation and deletion need authorization over the affected knowledge.
Authority can come from ownership, an authorized owning-group role, or an
applicable organizational hierarchy. It is not limited to the original author.
An employee's departure can justify deletion of knowledge under that
organization's authority. It does not grant control over unrelated private data.

Disagreement alone does not authorize invalidation of someone else's document.
Deletion must be possible; do not require all deleted knowledge to survive as
history. Detailed retention, propagation, and dangling-reference behavior remain
open. Later reconstruction cannot promise access to deleted evidence.

## Context enrichment

Magi can use relevant context permitted for the caller and, when applicable,
the persona and represented user. It can fetch live calendar data where useful.
Record what was fetched and when; a retained snapshot is not current forever.

Read access is not blanket permission to use a source for enrichment. Respect
the source's intended use and explicit settings. Private journals are not
routine enrichment sources. Explicit restrictions take precedence over inferred
use. Authorized journal work is different from unrelated context enrichment.

Record missing data without inventing it. Reflection, later interviews, and
new evidence can improve interpretations. Their schedules and decision rules
are deferred; they are not required for initial capture.

## Query scenarios

These examples define required behavior, not fixed query classes.

| Scenario | Required evidence and behavior |
| --- | --- |
| Spending on adult children this month compared with an earlier period | Use permitted family context, living arrangements, calendar clues, bank and Venmo records, and purchase explanations. Identify missing sources and uncertainty. |
| Project Foo campaign spending and daily conversions | Use consistent periods, metric definitions, source coverage, and reproducible calculations. Explain incomplete or incompatible data. |
| A lawn-camera alert caused no wake-up or task | Recover the inputs, assessment, policy, and actual handling. Distinguish a recorded non-action from delay, failure, or missing processing evidence. |
| Janaya explains a cross-domain LinkKeys calendar update | Use relevant documentation, implementation evidence, and example exchanges. Distinguish examples from observed messages and intended behavior from implemented behavior. |
| Recall a white-dwarf discussion from three weeks ago | Find the relevant part of an interleaved interaction and its task context without requiring the user to know a session ID. |

For the family-spending example, establish which adult children have moved out
during the relevant period. Three can share a home. A shared purchase must not
be counted once for each child. Reconcile a bank
transfer and the Venmo payment it funds before counting costs. A calendar
meeting near a purchase is a clue, not proof of its purpose. Ask the requesting
user or their spouse about unknown purchases when needed and permitted.

Precision follows the query's purpose. Exploratory comparisons can use ranges
and incomplete coverage. Missing accounts can make a range an estimate, not
a guaranteed bound. More exact analytical tasks need clear definitions and
calculation evidence. Do not assign precision solely by personal or business use.

User effort is part of the context. A user driving with a phone should not
need to complete bank MFA before receiving a useful estimate. Explain the gap
and defer access that is inconvenient or unavailable. Do not treat missing
credentials as authorization to obtain them through another route.

## Real time interaction

Graph segments can select overlapping interactions and source ranges. Keep
source evidence, speaker attribution, interpretations, and actual session inputs
distinct. A segment need not be a contiguous time interval or belong to one
task. Task sessions still bound work and privacy. Context selection must check
the permissions of records and relationship metadata before model disclosure.

Conversations, task sessions, agents, queries, and evaluations can run at the
same time. Interaction points present only part of that work. Magi tracks
pending questions, responses, channel use, and changes in attention.

Voice is a shared half-duplex interaction channel for the participating user.
Text interactions can proceed concurrently. A spoken question can receive a
typed answer. A typed answer can instead address another conversation. One
response can answer several questions and advance several sessions at once.

Analyze whether to separate a response, share its meaning, or seek clarification.
Preserve enough permitted context for each destination to understand its input.
Do not assume the last presented question is the only possible target. Restore
the topic cue when a delayed answer interrupts a switch to another conversation.
See [Interaction](interaction.md) for the required examples.

Channel choice depends on user settings and current context. Learning can help
later interpretation. Candidate modes can apply to a person, an interaction
point, a situation, or a combination. Examples include shower, desk, phone, or
car use when that context is available and permitted. No fixed mode model or
automatic learning schedule is selected.

Do not require a mode label before admitting an interaction. Do not use a mode
transition as the primitive for starting work or granting authority. Retain
analysis methods and source revisions when later processing assigns such labels.

## Evidence layers and later interpretation

Preserve at least these roles; they are not an exhaustive storage schema:

1. Original interaction evidence, including its source and available context.
2. Interpretations and routing decisions that associate it with questions or tasks.
3. The inputs actually delivered to each session and their resulting use.

Keep these records linked across stores. One interaction can support several
interpretations and several session inputs. Several interactions can support
one interpretation. A split response must retain its common origin. A shared
response must retain its joint meaning where that meaning is needed.

Presentation, interruption, correction, evaluation, and learning records can
add further layers. Record what was queued, presented, interrupted, or still
pending where available. Delivery is not proof of human attention. A transcript
alone cannot preserve all audio cues; retain permitted original media where
available and identify capture gaps. Apply the archive's secret-reference rules.

The user selected local post-processing by two or three transcription engines
on 2026-10-16. Retain candidate transcripts and disputed spans. Later agreement
can improve the preferred transcript without proving truth. Raw audio can expire
sooner under a bounded retention policy. Record deletion and the resulting limits
on future acoustic analysis. Do not rewrite the live transcript or rerun actions
when later processing changes an interpretation.

Preserve the earlier interpretation and actual inputs when adding a later
assessment. Do not rewrite what an agent received to match a better explanation.
Record corrections without implying that earlier external effects were undone.
A shared source does not merge sessions or grant access to sibling context.

Possible assessments include a hypothesis, likely intent, or a reason to
investigate mood or tone. Their weights can differ by interpretation, purpose,
and context. Weight is not authority or a universal truth score. Keep supporting
and contrary evidence and the basis for an assessment when available.

Proposed extension mechanism: link versioned assessment records to retained
source references. Record the assessor, method, applicable scope, and input
revisions. Allow additional typed dimensions without requiring them in the
first implementation. Do not fix an emotion taxonomy, scoring scale, or graph
engine now. Preserve distinctions needed for later review.

Reprocessing retained data is one way to add understanding, not the only way.
New interactions and human review can add evidence too. A new assessment uses
current access and processing policy. It does not replay commands, resend
messages, or silently change past decisions. Applying a new decision is a
separate authorized action. Unavailable or deleted inputs remain explicit gaps.

## Recall and unavailable services

The user target is a useful response within a few seconds. When recall takes
longer, acknowledge the lookup promptly and aim to retrieve the context in
about twenty seconds. These are design targets, not measured guarantees.
Define the workload and exact measurement thresholds before claiming success.

Historical recall must recover a relevant segment and its context, not just
a keyword match. An access-controlled locator index is a candidate mechanism.
The storage and retrieval plan remains open. Cached content must still satisfy
current access policy and the query's freshness requirements.

Distinguish no match, unavailable service, incomplete search, and denied access
where the caller is permitted to know the distinction. Do not invent a memory
when its source cannot be obtained. A new answer without the past context must
be identified as such.

A blocking outage must be visible during an active request, even at 03:00.
Quiet-hour rules for unsolicited alerts do not require silence about that
failure. Explain a deferred or failed alert only from available evidence.
Diagnosis needs a path that does not depend entirely on the failed store.
Recovery actions still require authority. Detailed recovery design remains open.

## Next Actions

Use these scenarios to test the [record and storage proposal](knowledge-storage.md).
Begin with source references, capture, interpretation links, and actual session
inputs. Add typed assessment details as needed. Test reassembly, privacy, channel
changes, recall, and service failure before adding a full reflection or adaptive
learning system.
