# conversation

The conversation primitive keeps the conversation graph. Utterances are
nodes. Links are edges from an utterance (or a part of it) to earlier
utterances and to records in other systems. A stream orders the utterances
of one person and one persona. The service stores and enforces; it does not
interpret. A classifier (the Magi librarian) proposes links and visibility,
and the person corrects them. See `docs/conversations.md` for the model.

## API

Contract: `contracts/conversation.csil`. Service: `ConversationService`.

| Operation | Function |
| --- | --- |
| `put-persona`, `get-persona`, `list-personas` | Personas with their sharing setting (`never`, `private`, `public`) and audience (`members`). `put-persona` needs `conversation:admin`. |
| `open-stream`, `get-stream`, `list-streams` | The stream of a person and a persona. `open-stream` makes it when it is not there. |
| `post` | Adds an utterance to a stream, with links. `seq` increases by one in each stream. A new utterance is private and not classified. |
| `list-utterances` | Utterances of a stream after `after_seq` (oldest first), or the newest before `before_seq`, with their current links. With `wait_ms`, the call waits for new utterances (maximum 30 s). |
| `get-utterance`, `get-utterances` | Utterances by id. |
| `search` | Words in readable utterances (PostgreSQL full text, any word), best match first. |
| `trace` | Follows links back, forward, or both, to a depth of at most 4. |
| `put-link`, `retract-link`, `list-links` | Adds, retracts, and lists links. |
| `set-visibility` | `private` or `shared`. |
| `list-reviews`, `answer-review` | The review queue of a person. |
| `list-examples`, `get-calibration` | The decisions of a person for a classifier, and the review threshold. |
| `claim-unclassified`, `mark-classified` | Work queue of a classifier, with a lease. |
| `annotate`, `list-annotations` | Records about an utterance, such as an interpretation log. |
| `import-legacy` | Copies the chats of the earlier model into streams. Needs `conversation:admin`. |

## Access

- Every call has an identity: its token. The readers of an utterance are
  the person of its stream and the persona; a shared utterance also has
  the audience of the persona (`audience:<persona>`). A subject reads as
  itself and as the audiences that it is a member of.
- A link is readable when the caller can read the utterance that it starts
  from and, for an utterance target, the target.
- A caller with `conversation:act-as` or `conversation:admin` reads all
  records. With `person` (and optionally `persona`) in the request, it reads
  as that person only (through that persona only). A persona turn uses
  this.
- The store does not filter by readers itself. PostgreSQL row-level
  security (`FORCE ROW LEVEL SECURITY`, so it applies to the table owner)
  filters utterances, links, reviews, and examples by the readers that
  each transaction sets. A bug in a query cannot read records of other
  people. The service role must not be a superuser and must not have
  `BYPASSRLS`; the tests run as such a role.
- Only the person of a stream (or an act-as caller) posts in it, and only
  they or a classifier change its links and visibility. A member of the
  audience can read the shared utterances of the stream.

## Classification and review

A classifier (a caller other than the person) gives a confidence with each
link and visibility:

- At or above the threshold of the person for the task (`link` or
  `visibility`), the proposal applies.
- Below it, a review opens for the person. A link is `tentative` until the
  answer; a visibility does not change.
- The person's own decisions have confidence 1. A classifier cannot
  replace them (409).

Each answer, and each correction by the person of a classifier decision,
is stored as an example and moves the threshold: an accepted proposal
lowers it by 0.01, a correction raises it by 0.05 (range 0.5 to 0.99,
start 0.75). Thus the person gets fewer questions when the classifier is
usually right.

Answers: a visibility review takes `private` or `shared`. A link review
takes the proposed kind (accept), `unrelated` (retract), or another kind
(the person's link replaces the proposal). `dismiss` closes a review with
no change.

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `-listen` | `MAGI_LISTEN` | `:8080` |
| `-auth-keys` | `MAGI_AUTH_KEYS` | none (required). Comma-separated issuer key files. |
| `-database-url` | `MAGI_DATABASE_URL` | none (required) |

## Run

```sh
MAGI_AUTH_KEYS=/etc/magi/issuer.json \
MAGI_DATABASE_URL=postgres://magi@db.example.local/conversation \
go run ./cmd/conversation
```

The service applies its schema at startup. A database of the chat model
gets its old tables renamed to `legacy_*` for `import-legacy`. `/healthz`
checks the database.

## Test

```sh
MAGI_TEST_DATABASE_URL='postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
```

The integration tests make a new database, owned by a new role that is not
a superuser, so that row-level security applies. They drop both at the
end. They skip when `MAGI_TEST_DATABASE_URL` is not set.

## Known limits

- The service does not delete records.
- Search is lexical. Embedding search (pgvector) comes later.
- Text and annotation body: maximum 1 MiB. Maximum 100 links and 100
  references for each post, and 100 audience members.
- A waiting call holds one HTTP connection. Each wake reads the database
  again.
