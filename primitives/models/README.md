# models

The model catalog of one installation. It lists the models that Magi can
use, with their endpoints, tags, attributes, costs, eval scores, and the
calibrations of decision questions. It keeps the default model selection
of each task. Magi asks it for the models of a call (`select`).

Contract: `contracts/models.csil`, service `ModelService`. Design:
`docs/models.md`.

## API

| Operation | Who | Result |
| --- | --- | --- |
| `put_model`, `delete_model` | `models:admin` | Adds, replaces, or removes a model |
| `score` | `models:admin` | Adds or replaces scores and calibrations (an eval result) |
| `put_task` | `models:admin` | Sets the default selector of a task |
| `get_model`, `list_models`, `get_task`, `list_tasks` | any valid token | Reads |
| `select` | any valid token | The models for a task and a selector, in order, with a reason for each |

## Selection

A selector has these fields. Each given field must match:

| Field | Meaning |
| --- | --- |
| `name` | This model first. The other models that match the other fields come after it as a fallback. |
| `only_name` | Only the named model, with no fallback |
| `all` | The model has each of these tags |
| `any` | The model has one or more of these tags |
| `none` | The model has none of these tags |
| `attributes` | The model has these attribute values |
| `max_input_cost`, `max_output_cost` | The most cost (micro-dollars for each million tokens) |
| `rank_by` | A score key; a higher score comes first, and a model with no score comes after the models with a score |

After the rank, a lower cost comes first, then the name.

`select` takes a task and a selector. The given fields of the selector
replace the fields of the default of the task. A task with no default uses
only the selector. With `strict`, a named model must also match the other
fields: Magi uses it for the requests of a person, so that a person can
narrow the default but not go past its limits (for example, its cost).

A disabled model is never selected.

## Keys

The catalog has no keys. `endpoint.key_ref` names a key that Magi has:
`provider` (the key of `MAGI_MODEL_*`) or `inference` (the token for the
inference hosts). Magi skips a model whose key it does not have.

## Configuration

| Variable | Meaning |
| --- | --- |
| `MAGI_LISTEN` | Listen address (default `:8080`) |
| `MAGI_AUTH_KEYS` | Comma-separated issuer key files |
| `MAGI_DATABASE_URL` | PostgreSQL (database `models`) |

## Tests

```sh
MAGI_TEST_DATABASE_URL=postgres://magi:magi-test@127.0.0.1:55432/postgres?sslmode=disable go test ./...
```

`server/select_test.go` tests the selection without a database.
`server/server_test.go` tests each operation through the HTTP transport.
