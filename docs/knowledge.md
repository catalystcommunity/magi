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

Each change has one of three operations.

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
that?".

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
| `history` | The changes of one record. Owners only. |

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

### Proposed lessons

A lesson can also start as a proposal in the knowledge graph. The graph
then keeps the corpus: the proposals, the approved lessons, and the
rejected lessons. A proposal is an entity of kind `lesson` with the
external system `magi-lesson-proposal` and the property `status`
(`proposed`, `approved`, or `rejected`). Its id is `lp-` and 8 hex digits
of the title, so a second proposal with the same title gives the first one.

- An operator call makes a proposal: `magi lesson propose -title T -area A
  -source S -text TEXT` (`propose-lesson`).
- `magi lessons -status proposed` lists the proposals (`list-lessons`).
- Only an administrator decides: `magi lesson approve ID` or `magi lesson
  reject ID` (`decide-lesson`). The flags `-title`, `-area`, and `-text`
  change the proposal before the decision. The operate scope of a service
  is not sufficient, because a lesson changes each later code change.
- A code change gets a proposal only when its status is `approved`. A
  lesson of the file is always approved.
- A rejected proposal stays in the graph, so the same lesson does not come
  back as a new proposal.

The proposals are data of one installation. To keep an approved lesson for
each installation, add it to the file.

### Lessons of the file

A reviewer adds a lesson at the end of the file: the next free id, a title,
the area, the source, and the lesson in short sentences. A test parses the
embedded file, so a bad header or a duplicate id fails `tools/check.sh`.

When a task starts a code change, Magi reads the lessons from the graph and
scores each one. A lesson of area "all code" gets 3 points. A lesson of an
area path that the title or the instructions name gets 5 points, but only
for the repository of Magi. A word of 5 or more letters that the lesson
title and the task share gives 1 point, up to 3 points. Magi keeps the
lessons with more than 0 points: the highest score first, then by id, at
most 12. Magi puts the lessons in the var `lessons` of the run, and the
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
