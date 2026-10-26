# Personas sessions and trust

Updated 2026-10-06. The interview established these boundaries. Record shapes
and enforcement mechanisms remain proposals; no runtime is implemented.

A persona is a persistent expression of Magi. A session bounds one task and
its context. Source policies constrain session privacy and processing. Magi
uses the evidence graph to assemble evaluation context outside worker agents.

## Persona identity

A persona has a voice for speech interfaces, personality instructions for all
interfaces, and areas of focus. An Executive Assistant can focus on schedules
and personal events. An Engineering Manager can focus on engineering work.
Magi selects a suitable persona to take responsibility for a subsystem.
Keep that assignment across interactions and configuration revisions. Do not
select a new personality for each turn without changing the assignment.

Each persona has a stable LinkKeys identity. Changes to its voice, instructions,
or focus create configuration revisions, not new identities. Replacing the
persona creates a different identity. Model, process, and host changes do not
replace the persona. Retain the identity and configuration used for each action.

Workers act under the responsible persona's identity. Tasks, agent instances,
and attempts have internal IDs for coordination and evidence. These IDs are
not LinkKeys identities. Temporary task or agent identities are outside scope
and are not a required future feature. Do not require changes across LinkKeys
or receiving services to introduce them.

A shared persona signature cannot identify which worker produced a record.
Proposed attribution uses coordinator and executor admission records to check
internal agent and attempt IDs. Do not trust a caller-supplied ID by itself.

The persona name does not grant permission. Receiving services authorize the
authenticated persona and any representation of another identity. Selected
SSH keys, JWTs, or other credentials can supply authentication to other tools.
Record that credential binding without inventing a LinkKeys identity for it.

## Shared personas

Users can share a persona. One Executive Assistant can serve two spouses while
keeping each person's private context separate from household and public data.
Sharing the persona does not combine the users' permissions.

For user work, identify the persona, the represented requesting user, and the
result audience. The persona accesses private or group sources on that user's
behalf. Each source service retains authorization. Permission to obtain data
does not alone permit disclosure to another audience.

This describes represented user work, not every Magi query. An authenticated
system can act under its own authority for a household, company, or domain.
Knowledge queries do not require a persona or represented human. Record the
actual actor, applicable scope, and purpose; do not invent a represented user.

## Conversations and sessions

| Concept | Boundary |
| --- | --- |
| Persona | Persistent interface, focus, responsibility, and authenticated identity |
| Conversation | Continuing interaction that can contain several sessions |
| Session | One task and its bounded interactions, context, and child work |
| Agent instance | A participant in session work, recorded separately from its persona identity |
| Environment | A resource for tool execution; its lifetime is not the conversation or session boundary |
| Interaction point | Voice, text, or another endpoint that can serve concurrent conversations and sessions |

Three questions about today's calendar can belong to one session. A later
question about dog biology starts another session, even in the same visible
conversation. A session can contain child tasks and agents. They remain part
of that session's privacy boundary.

Several sessions and conversations can remain active at once. Switching the
presented topic need not close the earlier session. Voice and text can proceed
in parallel. One response can supply inputs to several sessions, with shared
source evidence and separate routing and delivery records. A common source
does not make those sessions one task or combine their permissions.

Do not automatically copy one session's context into the next. Retrieve earlier
evidence or knowledge only when relevant and permitted. Preserve links between
sessions without making a causal link a grant to read its source.

An external harness can choose to combine tasks in one session. It then retains
the resulting combined context and privacy restrictions. The visible chat
layout is not proof that task contexts are separate. Session identifiers must
be explicit in native APIs and evidence records.

Preserve actual session inputs separately from later interpretations. See
[Interaction](interaction.md) for channel changes and shared replies, and
[Knowledge context and reinterpretation](knowledge-and-context.md) for later review.

## Session privacy

A persona has default privacy settings. The Executive Assistant can start
private to the requesting user, even when another household member uses the
same persona. Magi can broaden scope to household or public when the content
and applicable policies clearly permit it.

Source systems also supply privacy restrictions. The session inherits the most
restrictive requirements of its steps, including child work. A private-journal
step makes the context of other steps, such as calendar work, private too.
Further analysis in that session must retain that restriction. A persona's
default or a public starting scope cannot override a private source.

Private access in a public place remains private. Neither the physical setting
nor the chosen interface changes the source policy. Source access still needs
authorization; a privacy label is not permission to obtain the data.

Proposed representation: preserve source owners, audience restrictions, and
policy references, not just a public/group/private rank. Two different private
owners or groups can have different permitted readers. Ordinary access must
satisfy all applicable restrictions. Do not select one owner and discard another.

Update the effective restriction for the retained session context and its
derived views. Do not change an entire source calendar's policy merely because
one session used it. Check permission before each disclosure. A later privacy
change cannot recall information already delivered to an authorized recipient.

Proposed enforcement keeps current access restrictions separate from immutable
payloads. A restriction change does not rewrite signed evidence. A privacy
label captured earlier is not sufficient authority for access now.

Removing messages or making a routine context summary does not remove a source
restriction. The separate learning evaluation below can classify selected
learnings; it does not rewrite the session's evidence or working privacy.

## Scoped trust policy

Humans edit trust policy within the areas they control. A person can decide
trust for their own private information, but not for their spouse's private
information. Both can have authority to edit household policy. Their children
need not have that authority. Express these grants in policy, not fixed family
roles in code.

Keep permission to receive information separate from permission to process it.
A model API can be trusted to process private information for one control area
without being trusted for another. Private does not inherently mean local-only.
Apply source processing rules when selecting models, evaluators, and execution
hosts. An unavailable trusted endpoint does not authorize an untrusted fallback.

Proposed trust records name the control area, authorized policy editors,
processing destination or recipient, permitted use, and policy revision.
Each domain owns its policy. A foreign trust statement is evidence for local
evaluation, not an instruction to accept it.

Knowledge authority is also specific to the adopting context and subject.
Human authorship or publication does not impose a belief on another user.
Communication is not automatically authorship. Personal knowledge needs
acceptance in the context that uses it; deterministic knowledge can be accepted
directly. Acceptance does not grant source access or deletion rights.

## Administrator evaluation and learnings

The Magi administrator can inspect retained Magi evidence under its own system
authority. It does not need to represent each user for that evaluation. This
access is distinct from ordinary persona access on behalf of a requesting user.
It does not grant access to unrelated records in external source systems.

Administrator access does not remove source privacy or processing rules. An
administrator cannot use its archive access to select a processor that the
relevant source policy does not trust. Record administrative evaluations too.

Later evaluation can separate private, group, and public learnings from the
evidence. Classify each learning using its actual source dependencies and the
task's scope. Do not copy a private example or identifying detail into a public
learning. When classification is uncertain, retain the restriction.

Some general learnings can have a broader scope than the session in which they
were found. Record the classification decision, sources, and permitted audience.
Keep restricted source links and explanations protected even when a learning
can be shared. This does not publish the raw session or grant training rights.

## Evaluation outside worker context

Magi assembles context for tool-use and scope evaluation from the evidence
graph. Evaluation does not run within the proposing worker's own context.
The evidence graph can be links across stores, not one physical graph database.
Select the authoritative task scope, current plan, applicable policies, proposed
operation, credential references, and relevant observations.

The worker's explanation is evidence, not authority to change its scope. Apply
source privacy and processing trust to the assembled evaluation context. A
separate evaluation does not require a new LinkKeys identity or a new service
process. The evaluator's model and implementation remain choices.

Proposed EvaluationRecord fields bind the evaluated operation, selected evidence
revisions, context snapshot, policy revisions, evaluator, and decision. Record
missing evidence or uncertainty. The evaluation must not silently approve a
different operation from the one examined.

Tool approval and direct credential delivery remain composable. A credential
can have more authority than the task needs. Model judgment and scope review
do not narrow that credential's permissions. Declare the actual enforced
controls and the parts that depend on trusted behavior.

## Next Actions

Use these boundaries in the proposed [contracts](contracts.md) and
[acceptance tests](test-environments.md). Compare storage designs using the
knowledge and interaction scenarios. Keep observations, evaluated learnings,
and active session context separate. Preserve their source links and access rules.
