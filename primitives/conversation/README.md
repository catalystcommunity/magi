# conversation

The conversation primitive keeps a graph of interactions. It stores
conversations, utterances, sessions, and annotations. It does not interpret
them.

## API

Contract: `contracts/conversation.csil`. Service: `ConversationService`.

| Operation | Function |
| --- | --- |
| `create-conversation` | Makes a conversation. The owner is the caller. |
| `get-conversation` | Returns one conversation. |
| `list-conversations` | Returns the conversations that the caller owns or is a participant in. The most recently updated is first. |
| `post` | Adds an utterance. `seq` increases by one in each conversation. |
| `list-utterances` | Returns utterances after `after_seq`, oldest first. With `wait_ms`, the call waits for new utterances (maximum 30 s). |
| `get-utterance` | Returns one utterance. |
| `open-session` | Makes a session in a conversation. The default status is `open`. |
| `update-session` | Changes the title or status. Adds references. |
| `list-sessions` | Returns the sessions of a conversation. |
| `annotate` | Adds an annotation to an utterance. The author is the caller. |
| `list-annotations` | Returns the annotations of an utterance. |

Authorization:

- A conversation is visible to its owner, to its participants, and to callers
  with `conversation:act-as` or `conversation:admin`. All operations on a
  conversation and its records need visibility. If not, the service returns
  403.
- Only a caller with `conversation:act-as` can set `owner` on create, or set
  `author` or `author_kind` on post.
- The default `author_kind` is `human` for a user and `system` for a service.
  Permitted values are `human`, `persona`, and `system`. `modality` is `text`
  (default) or `voice`.
- `reply_to` and `sessions` must refer to records in the same conversation.

Long poll: a waiting call wakes when a post occurs on any replica. The
service uses PostgreSQL `LISTEN`/`NOTIFY` and also reads the database each
second. Thus a post is not lost if a notification is lost.

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

The service applies its schema at startup. `/healthz` checks the database.

## Test

```sh
MAGI_TEST_DATABASE_URL='postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
```

The integration tests make a new database and drop it at the end. They skip
when `MAGI_TEST_DATABASE_URL` is not set.

## Known limits

- The service does not delete records.
- Text and annotation body: maximum 1 MiB. Maximum 100 participants, 100
  sessions for each utterance, and 100 references for each request.
- `list-conversations` does not show all conversations to an admin. It shows
  only the conversations of the caller.
- A waiting call holds one HTTP connection. Each wake reads the database
  again.
