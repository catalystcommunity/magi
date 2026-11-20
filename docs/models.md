# Models for tasks

Magi selects the model of each call from the model catalog, a primitive
with an API (`primitives/models`). A task has a default selection. A
persona, a workflow step, or one request can give its own selection: a
model by name, or a class of models by tags and attributes.

## Catalog

Each model has:

| Field | Example |
| --- | --- |
| `name` | `gemma-4-12b-notetaker` |
| `description` | "Tuned for the note taker. Other tasks: not measured." |
| `endpoint` | `base_url` (`http://<host name>.revconn/v1` for an inference host that connects out), `model`, `key_ref` |
| `tags` | `local`, `hosted`, `small`, `chat`, `tools`, `schema`, `vision`, `decide`, `notetaker` |
| `attributes` | `base: gemma-4-12b`, `gpu: rx7600`, `context: 32768` |
| `cost` | micro-dollars for each million input and output tokens; 0 is free. `as_of_ms` and `source`: when and where the operator read the price |
| `scores` | eval results, for example `notetaker/data5-test: 0.93` |
| `calibration` | the temperature of each decision question (`record: 1.57`) |
| `enabled` | a disabled model is never selected |

The name tells the purpose. An adapter has the name `<base>-<purpose>`.
Any call can use it, but its description and scores tell what it is for.

## Tasks

A task is a name for one kind of model call. Magi uses these tasks:

| Task | Call |
| --- | --- |
| `persona-turn` | A persona reply (tools) |
| `persona.<id>` | The replies of one persona, for example `persona.engineering` (Archimedes, persona id `persona:engineering`); then `persona-turn` |
| `note-taker` | The note taker in a persona turn; then `librarian` |
| `librarian` | The librarian (classification of utterances) |
| `search-judge` | The check and filter of web searches; then `librarian` |
| `interrupt` | Which sentence a new message answers; then `librarian` |
| `egress-review` | A review of an egress flow that no rule decides |
| `decide` | A decision (`MagiService/decision`) |
| `step.code-change.work` | The apprentice of a code change (the model proxy of Magi) |

Magi uses the default of the first task in the list that has one. With no
default, a call uses the static chain of the environment
(`MAGI_PERSONA_MODELS`, `MAGI_LIBRARIAN_MODELS`). The static chain is also
the fallback when the catalog does not answer.

The operator sets a default with `put_task`. For example:

- `note-taker`: `{"all": ["notetaker"], "rank_by": "notetaker/data5-test"}`
- `persona.engineering`: `{"name": "glm-5.3", "all": ["tools"]}`: GLM
  first, then the other models with tools.
- `decide`: `{"all": ["decide"], "any": ["small"]}`: a decision model on
  a small GPU.
- `librarian`: `{"none": ["hosted"]}`: no hosted model for background work.

A workflow step uses the task `step.<definition>.<step>`. The code change
uses `step.code-change.work`. The model proxy of Magi
(`/runner/v1/chat/completions`) selects the models of that task for each
model call of the apprentice. With no default for the task, the proxy uses
the fixed model of the environment (`MAGI_MODEL_*`).

An operator can choose the model of one code change. `MagiService/start`
takes the field `model`. It is a JSON object for the apprentice:

- `{"name": "glm-5.3-coder"}`: a model of the catalog, by name.
- `max_tokens`, `max_steps`, `context_tokens`, `temperature`: optional
  limits of the run.

The CLI has a flag for it: `magi start -model '{"name": "glm-5.3-coder"}'
INSTRUCTIONS`. The apprentice sends the name to the proxy in the model
field of the request. The proxy then selects only that model. A person who
is not an operator gets `rpc.Denied`.

## One request

`MagiService/say` and `MagiService/decision` take `model`, a selector for
the model calls of that request. It refines the default of the task:

- `{"name": "glm-5.3", "only_name": true}`: only this model.
- `{"all": ["local", "vision"]}`: a local model that can read images.

For a person who is not an operator, a named model must also agree with
the default of the task (`strict`), and the cost limits of the default
stay. Thus a person can narrow the selection, but cannot spend more than
the operator permits. The note taker and the other background calls of a
turn use their own tasks, not the selection of the request.

## Selection

1. The named model, when it is enabled (with `strict`, when it also
   matches the other fields).
2. Then the models that match: with `rank_by`, the higher score first;
   then the lower cost; then the name.
3. Magi tries the models in this order. A busy inference host refuses, and
   Magi tries the next model.

Magi caches a selection for 30 seconds. Its log has the task, the models,
and the reason of each (`model selection`). Each answer has the model that
gave it (`Served`, in the turn log).

## Decisions on small GPUs

A decision model reads a prompt once and gives probabilities of a few
labels; it writes no text (`evals/decide/README.md`). A small model on a
small GPU can do this work, so the large GPUs stay free for the large
models. Give such a model the tags `decide` and `small`, and give the task
`decide` a default that prefers them.

The note gate (`docs/knowledge.md`, "Note gate") uses the task `decide`
to stop the note taker for messages with no fact.

## Prices

Prices of providers change. Give each price that is not 0 the date when
you read it (`cost.as_of_ms`) and the source (`cost.source`). `select`
adds "cost date unknown" to the reason of a model with no date, and "cost
stale (date)" when the price is older than 90 days. Read the price again,
then put the model again. `tools/models.py list` shows the price and its
date.

## Persona defaults

| Task | Selector | Why |
| --- | --- | --- |
| `persona-turn` | `{"name": "gemma-4-12b-vr1", "all": ["tools"]}` | The local models first (free), then the hosted models |
| `persona.engineering` | `{"name": "glm-5.3", "all": ["tools"]}` | Archimedes does engineering work, which needs the large model; then the local models |

## Scores from evals

An eval writes its results to the catalog with `score` (scores and
calibrations). The selection uses them through `rank_by`. A new model or
adapter gets a score only after an eval.
