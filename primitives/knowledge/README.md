# knowledge

The knowledge primitive keeps a graph of things that Magi knows. A node is a
thing, such as a person, a system, a calendar, or a fact. An edge is a
directed relation between two nodes, such as "owns" or "hosted-in". Evidence
links a node or an edge to the record that supports it.

The knowledge graph is not the conversation graph. It does not keep evidence
bytes; the artifacts primitive keeps them. A node can describe a record in an
external system with `external` (`system`, `locator`, `context`).

Example: Alice has three calendars in system A and her work calendar in
system B. Make one node of kind `system` for each system. Make one node of
kind `calendar` for each calendar, with `external.system` set to the system
node ID. Connect them with `owns` and `hosted-in` edges.

## API

Contract: `contracts/knowledge.csil`, service `KnowledgeService`.

| Operation | Behavior |
| --- | --- |
| `put-node` | With `id`: update that node. Without `id`: update the node that has the same owner, `external.system`, and `external.locator`. Otherwise: make a new node. Absent optional fields keep their stored values. |
| `get-node` | Return the node, its incident edges, and the evidence for the node and for those edges. |
| `put-edge` | Make or update the edge with the same owner, `from_node`, `to_node`, and `relation`. Both nodes must belong to the owner. |
| `link-evidence` | Make or update evidence for a node or an edge. The same subject, `ref_kind`, and `ref` update `stance` and `note`. |
| `query` | Find nodes by `text`, `kind`, and `near`. Return the edges between the found nodes. |
| `delete` | Remove a node (with its edges and evidence), an edge (with its evidence), or an evidence record. |

Query rules:

- `text` uses PostgreSQL full-text search (English) on the label, the
  summary, and the property values. A case-insensitive substring match is
  also used, so short words and names match.
- `near` gives the nodes that are at most `depth` edges from a node, in the
  two directions. The default `depth` is 1. The maximum is 3.
- The default `limit` is 50. The maximum is 500.

## Access

- A record has an owner. By default, the owner is the caller.
- The caller can read and change its own records.
- `knowledge:act-as` lets a service set `owner` and act on the records of
  that owner. For `query`, set `owner` to select the records of another
  subject. Without `owner`, `query` returns the records of the caller.
- `knowledge:admin` permits all operations.
- A record that the caller cannot access gives "not found". `delete` returns
  `deleted: false`.
- `scope` is a stored policy tag (default `private`). It does not change
  visibility yet. Only the owner (and act-as or admin callers) can see a
  record.

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `-listen` | `MAGI_LISTEN` | `:8080` |
| `-auth-keys` | `MAGI_AUTH_KEYS` | none (required). Comma-separated issuer key files. |
| `-database-url` | `MAGI_DATABASE_URL` | none (required). PostgreSQL URL. |

The process applies its schema at startup.

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

- Visibility is owner only. `scope` is not enforced.
- An edge connects nodes of one owner only.
- The text search uses the English configuration only.
- There is no history of changes. An update replaces the stored values.
