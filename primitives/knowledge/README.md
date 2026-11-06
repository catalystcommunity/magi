# knowledge

The knowledge primitive keeps what Magi knows. `docs/knowledge.md` gives the
design: the record types, the three kinds of change, access, and queries.

| Record | Holds |
| --- | --- |
| Entity | A thing with an identity: label, aliases, summary, properties, and an optional reference to a record in another system (`external`). |
| Claim | One statement: subject (an entity), relation, and an object (an entity) or a value. It has the time when it was true, a confidence, and evidence. |
| Episode | A thing that occurred, with references to its records (utterances, workflow runs, artifacts). |
| Procedure | How to do a thing. Each put makes the next revision. |
| Evidence | A link from a record to the record that supports, contradicts, or mentions it. |
| Change | One row of the history of a record. |
| Dispute | A reader who does not agree with a record. |
| Group | A set of subjects that can own and read records together. |

The knowledge store does not keep evidence bytes. The artifacts primitive
keeps them.

## API

Contract: `contracts/knowledge.csil`, service `KnowledgeService`.

| Operation | Behavior |
| --- | --- |
| `put-entity` | Returns the entity with the same owners and external reference, or with the same owners, kind, and label or alias (case is ignored). It adds the new aliases and the absent fields. Otherwise it makes an entity. An entity of kind `thing` takes the new kind. |
| `self` | Returns the entity of the caller (kind `person`, alias `me`). It makes the entity when there is none. |
| `assert` | Returns the current claim with the same owners, subject, relation, and object or value, and adds the evidence. Otherwise it makes a claim. |
| `change` | `fix`, `update`, or `retract` of a record (see "Changes"). |
| `merge`, `unmerge` | Moves the claims, episodes, procedures, and evidence of one entity to another. The label becomes an alias. `unmerge` reverts it. |
| `record-episode` | Makes an episode. With `id`, changes it and adds to its lists. |
| `put-procedure` | Adds the next revision of a procedure. |
| `link-evidence` | Adds or changes evidence of a record. |
| `dispute`, `resolve-dispute`, `list-disputes` | A reader disputes a record. An owner accepts or rejects it. |
| `put-group` | Makes a group, or changes a group that the caller owns. |
| `search` | Records that have any of the words of the text, and entities whose label is similar to a part of the text. Without text, the records that changed last. |
| `about` | One entity, its claims in the two directions, the entities that they name, its episodes and procedures, the evidence, and the open disputes. |
| `neighbors` | The entities at most `depth` (maximum 3) claims from an entity. |
| `timeline` | Episodes, claims, and changes in a period. |
| `history` | The changes of one record. Owners only. |
| `get` | One record of any type. |

### Changes

| Operation | Records | Effect |
| --- | --- | --- |
| `fix` | all | Changes the fields in `set` in place. The history keeps the old values. When a fix changes the object of a claim, the old object entity is retracted too if nothing else uses it (same owners, no external reference, no summary, no other claim, episode, procedure, or merged entity). The change names it in `after.removed`. |
| `update` | claims | Ends the claim (`valid_to_ms`, status `ended`, `replaced_by`) and makes the claim that replaces it. |
| `retract` | all | Removes the record from all queries. An entity takes its claims with it. With `erase`, the rows are deleted and the history keeps no content. |

A change of a claim marks the claims that have it in `derived_from` as
`stale`. A claim with no supporting evidence and no `authored_by` has
`supported: false`.

Claims are current by default. `at_ms` gives the claims that were true at
that time. `include_ended` also gives the ended claims.

## Access

- Each record has `owners` and `readers`: subjects (`user:...`,
  `service:...`) and groups (`group:...`). A group has members.
- Owners read and change a record. Readers read it and can dispute it.
- A caller can set owners only to itself and to the groups that it is a
  member of. A caller with `knowledge:act-as` acts for the subject in
  `act_for` and can set all owners.
- `knowledge:admin` permits all operations.
- A record that the caller cannot read gives "not found".
- A claim needs a subject and an object that the caller can read. Its owners
  are the caller by default, also when the entity has other owners. This is
  "an own claim" (`docs/knowledge.md`, "Access").

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `-listen` | `MAGI_LISTEN` | `:8080` |
| `-auth-keys` | `MAGI_AUTH_KEYS` | none (required). Comma-separated issuer key files. |
| `-database-url` | `MAGI_DATABASE_URL` | none (required). PostgreSQL URL. |

The process applies its schema at startup. It needs the `pg_trgm` extension
(a trusted extension: the owner of the database can make it).

A database of the first schema (tables `nodes`, `edges`, `evidence`) is
changed at startup: the tables become `legacy_nodes`, `legacy_edges`, and
`legacy_evidence`, and their rows are copied once into entities, claims,
and evidence with the same IDs.

## Run

```sh
MAGI_AUTH_KEYS=/etc/magi/issuer.json \
MAGI_DATABASE_URL=postgres://knowledge@db.example.local/knowledge \
go run ./cmd/knowledge
```

## Test

```sh
go vet ./... && go test ./...
MAGI_TEST_DATABASE_URL='postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable' go test ./...
```

The integration tests make a new database for each test and drop it after.
They use the real HTTP transport and the generated client. They skip when
`MAGI_TEST_DATABASE_URL` is not set.

## Limits

- The search is full-text (English) and similar-label search. There is no
  embedding search yet.
- `magi` does not yet keep a group for the audience of each shared persona.
- An investigation does not yet record episodes or procedures (see
  `docs/knowledge.md`, "Investigations").
