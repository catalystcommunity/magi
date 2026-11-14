# Models for tasks (proposal)

Status: a proposal for review. Nothing in this document is built.

## Problem

Today Magi selects a model by role. `MAGI_PERSONA_MODELS` is the chain of
the persona, and `MAGI_LIBRARIAN_MODELS` is the chain of the librarian and
the note taker. Each chain is a list of endpoints. Magi tries them in order.

This is not sufficient for these changes:

- One engine can serve a base model and adapters (the note-taker LoRA)
  with per-request LoRA. These are different models for the caller.
- Some calls need an ability that some models do not have: tool calls, a
  JSON schema, images, or label probabilities (`decide`).
- Models have different costs. Local models are free. GLM through Zen
  spends the balance of the person.
- Evals measure how good a model is for a task. The selection does not
  use these results.

## Proposal

### Catalog

The catalog is a list of models of one installation. An operator writes
it. Each entry has:

| Field | Example |
| --- | --- |
| `name` | `gemma-4-12b-notetaker` |
| `endpoint` | the inference host `ht0-studiodt`, or a provider URL |
| `base` | `gemma-4-12b` |
| `adapter` | `notetaker-lora-v3` (optional) |
| `purpose` | "Tuned for the note taker. Other tasks: not measured." |
| `abilities` | `chat`, `tools`, `schema`, `vision`, `decide` |
| `context` | 32768 |
| `cost` | `free`, or the price for each million tokens |
| `where` | `local` or `hosted` |
| `scores` | eval results, for example `notetaker/data5-test: 0.93` |

The name tells the purpose. An adapter model has the name
`<base>-<purpose>`. A call for a different task can use it, but the
catalog says that it is tuned for another task.

### Needs of a call

Each model call in Magi states what it needs. A need has:

| Field | Example |
| --- | --- |
| `task` | `note-taker`, `persona-turn`, `decide`, `plan-steps` |
| `abilities` | `schema` |
| `prefer` | `gemma-4-12b-notetaker` (optional) |
| `where` | `local-only` for private data (optional) |
| `max_cost` | the most that the call can spend (optional) |
| `latency` | `interactive` or `batch` |

### Selection

1. Remove the models that do not have the abilities, the `where`, or the
   cost of the need.
2. Put the `prefer` model first.
3. Sort the other models by their score for the task. A model with no
   score for the task comes after the models with a score.
4. When two scores are almost the same, put the lower cost first. For an
   `interactive` call, put the lower latency first.
5. Try the models in this order, as a chain does today. A busy inference
   host refuses, and Magi tries the next model.

Magi writes the model and the reason in the turn log (`phases`). Thus each
answer shows which model gave it and why.

### Scores from evals

Each eval writes its results in a file of scores (model, eval, split,
score, date). The catalog reads the file. Thus the selection uses measured
results. A new adapter or model gets a score only after an eval.

## Changes

- The inference host serves more than one model name from one engine:
  the base, and adapters with per-request LoRA (`--lora-init-without-apply`).
  `GET /v1/models` gives the purpose of each name.
- The inference host gets `decide` (typed questions, label probabilities,
  a temperature for each question). See `evals/decide/README.md`.
- The catalog and the needs replace `MAGI_PERSONA_MODELS` and
  `MAGI_LIBRARIAN_MODELS`.

## Questions for review

1. Is the catalog a file of the operator (like `deploy.env`), or a
   primitive with an API, so that Magi can add a model after an eval?
2. Is `local-only` necessary now, or later?
3. Can a person change the selection for their own calls (for example,
   "do not spend Zen on the librarian")?
