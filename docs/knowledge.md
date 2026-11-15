# Knowledge

This document tells how Magi keeps what it knows, how it finds it, and how
a person changes it. The knowledge primitive (`primitives/knowledge`,
`contracts/knowledge.csil`) keeps the records. The `magi` service uses them
in the persona tools and in investigations.

## Rules

1. Magi points to the records of other systems. It does not copy a whole
   system. It keeps entities, links, dated claims, and short searchable text,
   each with a reference (`external`) to the record in its source system.
   The source system is the authority. An answer reads the source again when
   the source is available.
2. One entity has one identity in all systems. Magi merges two entities on
   an exact identifier (an email address, a LinkKeys subject, an external
   locator) without a question. Magi suggests a merge on a near match (the
   same name, a similar label). Magi never merges on a name only. A merge is
   a recorded change, and Magi can undo it.
3. The schema is open. Kinds of entities and names of relations are text.
   Only the record types and the ownership fields are fixed.
4. A change says what it is: a fix, an update, or a retraction (see
   "Changes"). A reader never follows a chain of corrections.
5. Only an owner changes a record. A person who can read a record and is not
   an owner can dispute it or make an own claim.

## Record types

| Type | Memory | Holds |
| --- | --- | --- |
| Entity | | A thing with an identity: a person, a place, a system, a device, a document, an event, a project, a model. Label, aliases, summary, properties, and an optional external reference. |
| Claim | semantic | One statement about an entity: a relation to another entity or to a value. It has the time when it was true (`valid_from_ms`, `valid_to_ms`), a confidence, and evidence. |
| Episode | episodic | A thing that occurred: a conversation, an investigation, a task, an observation, an import. It has a time, the participants, references to the records of the event (utterances, workflow runs, artifacts), and a result. |
| Procedure | procedural | How to do a thing: a goal, the inputs, the steps (text or a workflow definition), and a revision. Episodes that used a procedure record the result, so Magi knows which procedures work. |
| Evidence | | A link from a record to the record that supports it, contradicts it, or mentions it. |

Other links:

- A claim can have `derived_from`: the claims that a calculation used. When
  one of those claims changes, the derived claim is marked stale.
- An episode can name the procedure that it used.
- The conversation graph keeps what people said. An episode refers to
  utterances. It does not copy them. A `corrects` link in the conversation
  graph is a record of what a person said. The knowledge store keeps only the
  result.

Each owner has one entity of kind `person` for the self, with the alias
`me`. A personal fact is a claim on that entity, for example
`me —dentist→ Dr. Rivera`.

### Models

A trained model (for example an interruption classifier) is an entity of
kind `model`. Its properties give the version, the base model, and the
evaluation result. Its owners and readers give its scope: one person, a
household, or all people. Claims and evidence link a model to the episodes
that trained it. The persona uses the most specific model that the person
can read. This is a convention only. No part of the knowledge primitive is
specific to models.

## Changes

Each change has one of three operations (and `undo`, below).

| Operation | Use it when | Effect | Readers see |
| --- | --- | --- | --- |
| `fix` | The record was wrong when Magi wrote it: a word that speech to text heard wrong, a wrong value from a model, a typing error. | Changes the record in place. Writes the old value to the history. | The new value only. |
| `update` | The record was true, and now it is not. Example: "We changed dentists." | Ends the old claim (`valid_to_ms`) and adds a new claim. Only claims have updates. | The current claim. The old claim is in a question about the past. |
| `retract` | The record was never true, or the person wants Magi to forget it. | Removes the record from all queries. With `erase`, the history keeps no copy of the content. Claims derived from it become stale. | Nothing. |

A fix of the object of a claim also retracts the old object, when only that
claim used it. Example: "Dana lives in Tucson" becomes "Dana lives in
Phoenix", and "Tucson" was made only for the wrong claim. Else the error
stays in the store as a thing with no facts. A retraction of a claim also
retracts its object, when only that claim used it.

Evidence has the same operations. When the last supporting evidence of a
claim is retracted, and no person wrote the claim, the claim is marked
unsupported. The persona does not tell an unsupported claim as a fact. It
asks.

How the persona selects the operation:

- When the person says which operation, the persona does that operation.
  "That is wrong", "I said Tuesday": `fix`. "We moved it", "now": `update`.
  "That never occurred", "forget it": `retract`.
- When the words do not make it clear, the persona asks one question: "Did
  it change, or was it wrong?"
- When Magi finds an error itself (for example a source system has a
  different value), it does a `fix` only for a value that it wrote from that
  source. Otherwise it asks the owner.

The history (`history`) keeps each change: the operation, the person, the
time, the reason, the old value, and the utterance of the request. A normal
query does not read the history. The history answers "why did you think
that?". It is also the audit log (next section).

## Audit and undo

Every change of memory and knowledge writes one row of the `changes` table
(risk remediation 3.3, requirement 9.3). The table already existed (the
history); the service only added columns, so there is one log, not two.

| Field | Says |
| --- | --- |
| `actor` | The subject that the call acts as: the person, also when Magi writes for them. |
| `via` | The caller, when it is another subject (`service:magi`). Absent when the person called. |
| `origin` | JSON from the caller. Magi gives `persona`, `tool`, `turn` (the utterance), `run`, `by` (`api` for the API call of the person, `note-taker`), and `confirmed` (the person confirmed this exact action). |
| `trust` | The trust label of the entity or claim after the change. |
| `operation`, `record`, `at_ms`, `reason`, `utterance` | What, to which record, when, and why. |
| `before`, `after` | JSON of the changed fields: enough to undo the change. |
| `undoes`, `undone_by` | The link between an undo and the change that it undid. |

Rows are written for `create` (including the claim that an `update` makes),
`fix`, `update`, `retract`, `promote`, `merge`, `unmerge`, `edit` (an entity or
an episode changed in place, with the old values), `visibility` (the readers
of an entity, or the members of a group), `assert` (the same fact again, or
new evidence), `dispute`, and `undo`. A `retract` with `erase` keeps no
content, but its row stays.

- **Read it.** `history` with a record id gives the changes of that record.
  `history` with an empty id gives your audit log: the changes of all the
  records that you own, newest first (`since_ms`, `limit`). In the CLI:
  `magi history ID` and `magi history [-days N] [-n N]`. Owners read it;
  `knowledge:admin` reads all.
- **Undo.** `change` with operation `undo` and the id of a change restores
  the state before that change, and writes a row of its own (`operation`
  `undo`, `undoes` set; for a merge, the `unmerge` row). In the CLI:
  `magi undo CHANGE [-m REASON]`. What it restores: `create` (the record is
  retracted), `fix` (the old values, and an object entity that the fix
  retracted), `update` (the old claim is current again with its old end, the
  new claim is retracted), `retract` (each record with the status that it had,
  also an ended claim), `promote` (back to the old trust), `merge`
  (`unmerge`), `edit` and `visibility` (the old fields and readers).
- **Who.** The person for their own records (a `user` token that does not act
  for another subject), an admin (`knowledge:admin` on a `user` token), or a
  service that holds `knowledge:undo` by name (`*`, `knowledge:*`, and
  `knowledge:admin` do not give it). Magi holds the scope and calls undo only
  in the API call of the person (`change-knowledge` with `undo`, which refuses
  a caller that is not a `user`). A persona and a model have no undo: the tool
  `change_knowledge` does not take it, and `changeRecord` refuses it without the
  mark of the person.
- **Guards.** Undo is refused when:
  - a later change of the same record has not been undone (the answer names it:
    undo the later changes first, newest first);
  - records depend on the change: claims that name a created entity, claims
    derived from a created claim, episodes that used a created procedure;
  - the change was undone already, or is an undo or an unmerge (make the
    change again);
  - the change was a `retract` with `erase`, or is of a kind that has no
    undo: `assert`, `dispute`, and the changes of groups (change the group
    again);
  - it would raise trust. A `fix` that lowered a record to `untrusted` stays
    lowered; only `promote`, by the person, raises it. Undo of a `promote`
    lowers it again. After each undo, a check compares the trust of the record
    before and after.
  A record that the undo brings back must have its entities: a claim does not
  come back under an entity that is retracted.
- **Not covered.** Evidence rows that a retract of a claim leaves in place are
  not changed by undo. The audit rows of an erased record keep no before and
  after. Rows from before this migration have no `via`, `trust`, or `origin`;
  they sort first, and an old `update` row has no old end time, so undo gives
  the claim no end time.
- **Existing data.** The migration only adds nullable columns, a sequence
  for the order of rows, and two indexes (`store/schema.sql`), with
  `IF NOT EXISTS`, so it is safe to run at each start. Old rows are not
  rewritten.
- **Deploy.** Mint the token of the Magi service with `knowledge:undo` (and
  `knowledge:promote`) next to `knowledge:act-as`
  (`deploy/README.md`). Without it, undo through Magi fails with a
  permission error.

## Trust

Each entity and claim has a `trust` value that says where it came from
(requirement 8 of the risk remediation, task 2.4).

| trust | Meaning |
|---|---|
| `person` | The person said it, or promoted it. |
| `named` | A configured source. |
| `own` | Magi wrote it from the person's own inputs. This is the default. |
| `untrusted` | Text of others, of the web, or of a task. |

- **Existing data.** The column is added with `ADD COLUMN IF NOT EXISTS ...
  DEFAULT 'own'` at start-up (`store/schema.sql`). Records that exist before
  the migration read as `own`: Magi wrote them for the person and there is no
  record of a source. Nothing is rewritten, and the migration is safe to run
  again. A record that an older service returns with no `trust` is shown by
  Magi as unverified (fail closed).
- **Where untrusted records come from.** A tool call in a turn that read
  untrusted text is held for confirmation (`magi/compose/confirm.go`). When
  the person confirms, the write is stored as `untrusted`: they allowed Magi to
  keep the text, they did not vouch for it. The note taker goes through the
  same path. The librarian writes no memory.
- **Stored apart.** `untrusted` records have a partial index and the
  `trust` column. A write with a lower trust never changes a record with a
  higher trust: an untrusted write that names an existing entity changes none
  of its fields, the same fact from an untrusted source adds no evidence to
  a trusted claim, and an untrusted entity cannot merge into a trusted one.
  An update keeps the trust of the claim, or lowers it.
- **Shown quoted.** `recall` lists untrusted records in a separate section,
  each line marked `(untrusted, unverified)` and quoted. The prompt of a turn
  (`knowledgeContext`) leaves them out. The persona rules say that they are
  never instructions.
- **Promote.** `change` with operation `promote` moves an `untrusted` entity
  or claim to `person`. The service allows it only for the person as
  themself (a `user` token that does not act for another subject) or for a
  service that has the scope `knowledge:promote` by name (`*`,
  `knowledge:*`, and `knowledge:admin` do not give it). Magi holds that scope
  and calls promote in two places: the API call of the person
  (`change-knowledge`, which refuses a caller that is not a user), and the
  confirmation of a pending action that shows the stored record
  (the message `confirm <code>` of the person). A model that calls
  `change_knowledge` with `promote` only makes a pending action. A `fix` can
  lower trust (to `untrusted`), never raise it.
- **Deploy.** Mint the token of the Magi service with `knowledge:promote`
  next to `knowledge:act-as`. Without it, promote through Magi fails with
  "only the person can promote".

## Access

- Each record has `owners` and `readers`. An item is a subject (`user:...`)
  or a group (`group:...`). A group has members. The `magi` service keeps
  one group for the audience of each persona that is shared with a
  household.
- An owner reads, changes, and disputes the record. A reader reads it and
  disputes it.
- A person can make records owned by the self and by groups that the person
  is a member of. A service with `knowledge:act-as` can set other owners.
- A dispute goes to the owners as a notice. An owner accepts it (with a
  change) or rejects it.
- A person who does not agree with a claim can make an own claim. When two
  claims that a person can read do not agree, the persona tells both and
  tells who owns each one.
- A record from another system also obeys the access rules of that system.
  The adapter gives the readers when it writes the record, and `fetch`
  checks them again.

## Queries

The persona and programs use typed operations, not a query language:

| Operation | Gives |
| --- | --- |
| `search` | Records that match text, filtered by record type, kind, relation, and time. Full-text search with any of the words, and similar-text search on labels and aliases. |
| `about` | One entity: its claims in the two directions, its recent episodes, the procedures about it, and open disputes. |
| `neighbors` | The entities that are at most `depth` claims from an entity (a recursive query in PostgreSQL). |
| `timeline` | Episodes, claims, and changes in a period. |
| `history` | The changes of one record, or with no id the audit log of the caller. Owners only. |

Claims are current by default. `at_ms` gives the claims that were true at a
time. `include_ended` also gives ended claims.

The October tests (`~/agent-learning/magi-retrieval-comparison.md`) found
that explicit links find a correction that text search does not find, and
that a model that writes the search did not help. Thus links are made when
data comes in, and the search is simple. Embedding search is the next step
when a test set shows that it is necessary.

## Note taker

A persona model can say "I recorded it" and not call a tool. A small local
model does this often. A second request to the model does not repair it:
the model then apologizes in later replies. Thus Magi records facts in a
separate step, the note taker:

1. Magi finds the records that match the newest message: at most four
   entities with their facts. When the message speaks of the person ("I",
   "my") and the person's own entity matches, that entity is first, then
   the things that its matching facts name ("my dentist": Dr. Rivera).
   A fact shows once, and again under the thing that it points to, so
   that a thing lists all that is in it.
2. The note taker model gets the records, the two messages before, and the
   newest message. It gives a list of operations: `remember`, `update`,
   `fix`, or `retract`, as JSON that agrees with a schema.
3. Magi applies each operation with the functions of the tools. The access
   rules and the conflict rule of `remember` apply.
4. The persona model gets a list of what Magi did, and writes the reply.

### Note gate

The note taker uses the large model, and most messages have no fact
(questions, requests, small talk). The note gate asks a decision model
(task `decide`, a small GPU) the question `record` of
`evals/decide/eval_notes.py`: "Does the message tell something new that
the notes must keep?" The gate and the note taker start at the same time.
When the probability of yes is less than `MAGI_NOTE_GATE` (default 0.15),
Magi stops the note taker, and the turn has no note operations. With no
answer in 5 seconds, or with an error, the note taker continues. 0 turns
the gate off. The turn log has the answer (`gate`: yes, model, ms, skip).

On data5 test with 0.15: Gemma 4 E4B with the decision LoRA
(`gemma-4-e4b-decide`) stops the note taker for 26.1% of the messages
(26.9% have no fact) and misses 0.4% of the messages that have a fact
(3 of 729). Plain E4B stops 18.6% and misses 4.1%. data5 has more facts
than real conversations, so the gate stops more in real use. A missed fact is usually not clear in the
message; the persona tools can still record it. The question and the
state must stay the same as in the eval, because the calibration of the
catalog is for them (`notegate_test.go` checks this).

The note taker gives no operation when the words do not make clear if a
fact changed or was wrong. Then the persona asks. `update`, `fix`, and
`retract` need an id from the records. The persona tools stay: the
persona can record and change facts, also in later turns.

For a question about the past ("before", "used to", "previously"), the
records include the facts that ended.

The note taker can give the wrong operation or the wrong fact. Magi
corrects two errors before it applies an item:

- The operation: words of a mistake ("correction", "I misspoke", "that's
  wrong", "oops") make a `fix`. Words of a change ("now", "switched",
  "moved", "this year"), with no words of a mistake, make an `update`.
- The fact: when the message names the old value ("in the garage, not the
  basement"), the fact must have it. When the fact of the id does not have
  it, and one other fact of the records has it, Magi changes that fact.

A `retract` item also names the thing to forget. Magi does not apply it
when the record of the id does not have each word of that name. Example:
"There is no chores calendar" must not retract the family calendar.

## Fact shape

Small models give facts in shapes that the graph cannot use. Before
`remember` records a fact (from the persona or from the note taker), Magi
changes the fact to the shape of the graph:

| Model gives | Magi records |
| --- | --- |
| `me vet "Dr. Okafor at Birch Lane Animal Clinic"` | `me vet Dr. Okafor` and `Dr. Okafor works-at Birch Lane Animal Clinic` |
| subject `my work calendar` | subject `work calendar` |
| object `March 3` | value `March 3` (a date or a number is a value) |
| object `household Nextcloud` when `Nextcloud` exists | object `Nextcloud` (the same entity) |
| `me job Brightline Logistics` (an object) | `me works-at Brightline Logistics` |
| `family calendar located-in` value `household Nextcloud` | object `household Nextcloud` (a relation of a thing points to a thing; a date stays a value) |

Two names are the same entity when they have the same proper name and
differ only in the lower-case words before it. "family calendar" has no
proper name, thus it does not join other calendars.

A fix or an update of the object of a fact has the same shape: "Dr. Chen at
Oak Hill Dental" becomes Dr. Chen, and Dr. Chen works-at Oak Hill Dental.

When a note taker item has both an object and a value, Magi keeps one:
for a relation of a thing (`located-in`, `hosted-in`, `books-through`), the
object becomes the subject and the value the object. For `works-at`, the
value is a second fact (`me job operations manager`). For other
relations, the value goes. When the subject and the object are the
same, the value is the object.

When the note taker gives the same fact about the person and about another
subject in one message ("Dana's birthday is March 3"), Magi keeps only the
other subject. Words such as "both" or "we" keep both.

## Review lessons

A review lesson is what a code review taught: a rule that the next change
must obey. The lessons live in the repository, in
`magi/compose/baseknowledge/review-lessons.md`. The repository is the source
of truth. Each lesson has an id (`rl-` and four digits), a title, an area, a
source (the run id of the review and its date), and one to four sentences.

When `magi-server` starts, it copies the lessons into the knowledge graph of
that installation (`ImportBaseKnowledge`): one entity of kind `lesson` for
each lesson, with the title as the label, the text as the summary, the id,
the area, and the source as properties, and an external reference to the
file. The `magi` service owns the entities. A lesson that changed in the
file gets a `fix`; a lesson that the file no longer has gets a `retract`.
Production gets the same lessons from the same release. The knowledge graph
is a copy that Magi can search; it is not the authority.

A reviewer adds a lesson at the end of the file: the next free id, a title,
the area, the source, and the lesson in short sentences. A test parses the
embedded file, so a bad header or a duplicate id fails `tools/check.sh`.

When a task starts a code change, Magi reads the lessons from the graph and
scores each one. A lesson of area "all code" gets 3 points. A lesson of an
area path that the title or the instructions name gets 5 points, but only
for the repository of Magi. A word of 5 or more letters that the lesson
title and the task share gives 1 point, up to 3 points. Magi keeps the
lessons with more than 0 points: the highest score first, then by id, at
most 8. Magi puts the lessons in the var `lessons` of the run, and the
apprentice shows them in its first message, before the task instructions.
If the knowledge service fails, the code change starts without lessons.

## Investigations

A question that needs data from many systems, calculations, or answers of
other people is an investigation. Examples: the failures of a disk array in
the last week, the quantity of food for a party, a hiring plan from sales
data. An investigation is a workflow that Magi writes for the question:

1. Plan: find the entities, the systems that have the data, and a procedure
   for the goal. Write a workflow definition.
2. Gather: read the source systems through adapters (API or command).
3. Compute: do the calculations in an execution environment. A model does
   not do arithmetic.
4. Ask: send a question to a person, and wait for the answer or a time
   limit. This needs a human step that names the person who must answer.
5. Report: give the answer with its evidence, the alternatives, the
   assumptions, and the next questions.
6. Learn: record an episode. Record the results as claims with
   `derived_from`. When the person approves, keep the plan as a procedure.

## Read-only runners

An investigation reads data on a server (for example the disk array of a
storage server) through a runner on that server. The runner connects out to
Magi. A runner for reading obeys these rules:

- The runner has a command list. Each item is a program and the arguments
  or subcommands that it permits, for example `mdadm --detail`,
  `smartctl -a /dev/sd?`, `journalctl -k --since`. The runner refuses a
  command that is not in the list. It runs the program directly, not
  through a shell.
- The runner keeps the list in its own configuration on the server. Magi
  cannot change it. To add a command, Magi sends a request. A person (not
  the agent that asked) reviews it and changes the list on the server.
- The runner runs as a user with no write access to the system, with
  `NoNewPrivileges`, a read-only file system view, and no network except
  the connection to Magi. A command that needs root (for example
  `smartctl`) uses a fixed `sudo` rule for that command line only.
- The runner records each command, its arguments, its exit status, and the
  hash of its output in the episode.

This design is a plan. It is not implemented yet.

## Tests

`evals/knowledge/` has frozen evaluation sets and a runner. A set says
facts to a persona in conversation, asks questions, does changes, and
checks the replies. Each run uses a new subject. See
`evals/knowledge/README.md`.
