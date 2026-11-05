# Conversations as a graph

A person talks with a persona as with a person. There are no chats to
start, name, or choose. The person asks a question, continues an earlier
thought, or changes the subject, and the persona follows. The text view
and voice show one natural conversation. Under the view, the conversation
is a graph.

The conversation graph is not "memory". It is a graph of what was said,
with the relations between the parts. Magi queries it for context and for
analysis. Memory is a concept: episodic and procedural memory can come from
this graph, from the knowledge graph, and from other systems (a calendar,
for example). Memory is not a primitive.

## Terms

| Term | Meaning |
| --- | --- |
| Persona | An identity that people talk to, for example Magi or a house persona ("the butler"). A persona has a sharing setting. |
| Stream | The utterances of one person and one persona, in the order that they occurred. The view shows the stream. A stream is an order, not an access boundary. |
| Utterance | A node: one thing that someone said, typed, or did. Its text does not change. Its readers can change. |
| Link | A directed edge from an utterance (or a span of it) to an earlier utterance or to a record in another system. A link has a kind, a weight, a confidence, and an author. |
| Trace | A path through links. An utterance can be on many traces. |
| Reader | A principal that can read a node: a person, a persona, or a group (for example the audience of a persona). |
| Review | A question to a person about a classification that has a low confidence. |

## Model

```text
u1  tod  "the readme on the parakeet repo is wrong"
u2  magi "I started task run_x to fix it"        reply-to u1, started workflow-run run_x
u3  tod  "also the pi runner was slow"            changes-subject-from u2
u4  magi "The Pi was building Rust ..."           answers u3
... two days ...
u9  tod  "did that readme thing get fixed, and is the pi faster now?"
          asks-about u2 (span 0-30, weight 0.5), asks-about u4 (span 35-60, weight 0.5)
u10 magi "Yes, run_x merged. The Pi ..."          answers u9, reports-on workflow-run run_x
```

- One utterance can have links to many targets. A span (character offsets)
  and a weight (the part of the utterance that the link applies to) tell
  how the utterance divides between the targets. Without a span, the link
  applies to all of the utterance.
- Utterance text is immutable. A link is a record about utterances. A
  correction makes a new link that replaces the old link, or retracts it.
  The history stays.
- There are no thread records. A trace is a query: follow the links from
  an utterance. Topics and summaries belong to the knowledge graph, which
  can link to utterances as evidence.

### Link kinds

The kind is open text (`^[a-z][a-z-]{1,39}$`). These are the usual kinds:

| Group | Kinds |
| --- | --- |
| Turn | `reply-to`, `answers`, `asks-about`, `acknowledges`, `thanks` |
| Flow | `continues`, `resumes`, `follows-up`, `changes-subject-from`, `same-subject-as` |
| Content | `elaborates`, `clarifies`, `restates`, `summarizes`, `quotes`, `example-of`, `refers-to` |
| Stance | `agrees-with`, `disagrees-with`, `contradicts`, `confirms`, `corrects`, `contrasts-with` |
| Action | `requests`, `started`, `reports-on`, `decides`, `fulfills`, `cancels` |
| Knowledge | `about`, `mentions`, `evidence-for` |
| Speech | `interrupts`, `reads-ahead`, `cuts-off` (see below) |

A link target is an utterance, or a reference to another system:
`workflow-run`, `knowledge-node`, `artifact`, `url`. A link to an utterance
can have a `target_span`: the part of the target text (code point offsets)
that it points at.

### Interruptions of spoken replies

When a person stops a spoken reply and then says something, the persona
turn links the new message to the reply (`magi/compose/interrupt.go`):

| Kind | When | `target_span` |
| --- | --- | --- |
| `interrupts` | The message answers a sentence that the person heard | That sentence |
| `reads-ahead` | It answers a later sentence, and the text was on the screen long enough to read it (4 words a second) | That sentence |
| `cuts-off` | It answers nothing in the reply | The word where playback stopped |

The data comes from the `delivery` annotation of the client
(`report-delivery`: `spoken_until`, `shown_ms`, `stopped_ms`, `reason`).
The local model picks the sentence (a schema permits only the sentence
numbers and -1). The note of the link has the heard length. The persona
then sees its reply as the person got it: the heard part and a note that
the person stopped it, or all of it after `reads-ahead`.

## Access

- Every query has an identity: the token of the caller. A query returns
  only the nodes and links that the identity can read. There is no
  partition key in the request.
- An utterance has readers. A link is readable when the caller can read
  the utterance that it starts from and, for an utterance target, the
  target.
- The service enforces this in two places: in the service code, and with
  PostgreSQL row-level security. A bug in a query cannot read nodes that
  the identity cannot read.
- A service with the `conversation:act-as` scope can read all nodes (the
  librarian is such an identity), or it can query as a person (`as`)
  through a persona (`via`). A persona turn uses `as` and `via`. Thus the
  persona sees what this person told this persona, and what is shared
  with the audience of the persona that the person is a member of. It does
  not see what the person told a different persona.

### Persona sharing

Each persona has a sharing setting:

| Setting | Meaning |
| --- | --- |
| `never` | All utterances are private to the person and the persona. |
| `private` | Private by default. An utterance becomes shared when the classifier is sure that it is for the household ("tell everyone the plumber comes on Tuesday"), or when the person says so. |
| `public` | Shared with the audience by default. An utterance stays private when the classifier finds that it is personal, or when the person says so. |

The audience of a persona is a list of members. Magi is `private`. A
butler persona can be `public`, with the household as its audience.

A new utterance is private until it is classified. Thus a classification
that is not done yet cannot share a personal thing. A low-confidence
classification keeps the utterance private and asks the person.

## Classification and review

The librarian is a background job on a local model. After each utterance
(and for each imported utterance), it proposes links and a visibility,
each with a confidence. It gets as examples the recent answers and
corrections of the same person.

- A proposal with a confidence at or above the threshold of the person
  for that task is applied. The person can see and correct it later
  (audit).
- A proposal below the threshold makes a review. The review queue of the
  person shows it. The proposal does not apply until the person answers.
  Exception: a link proposal below the threshold is stored as a tentative
  link, which a trace uses only on request.
- An answer, or a correction of an applied item, becomes an example for
  the classifier and changes the threshold: an accepted proposal lowers
  the threshold a little; a correction raises it more. Thus the person is
  asked less when the classifier is usually right, and more when it is
  often wrong.

## Reply flow

1. The person says something (text or voice).
2. Magi gets context, as the person through the persona:
   - the newest turns of the stream;
   - utterances that match the words (lexical search), with their
     traces;
   - ids in the text (tasks, utterances).
3. The persona replies. Its reply has a `reply-to` link. The reply has
   the visibility of the utterance it replies to.
4. The librarian classifies the new utterance and the reply.

## Views

- Text: one continuous stream, newest at the bottom, with no chat list. A
  message that the librarian linked to an earlier message shows an
  "about" line ("asks about: the readme on the parakeet repo, Tuesday").
  Tap it to go to that message. Search covers all the readable history.
- Voice: the same stream.
- Review: a list of open reviews, each with its options.

## Import

`import-legacy` copies the chats of the earlier model into streams: one
stream for each person and persona, in time order. The utterance ids stay
the same, so knowledge evidence still refers to them. `reply_to` becomes a
`reply-to` link, and the task sessions become `reports-on` links. The
librarian then classifies the imported utterances. This is the first test
of the classifier.

## Later

- Embedding search (pgvector is in the database) on a local embedding
  model.
- The knowledge graph: topics with summaries, episodic and procedural
  records, all with evidence links to utterances.
