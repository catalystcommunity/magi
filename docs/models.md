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
| `step.code-change.backup` | The backup model of a code change ("Backup model") |

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
- `max_tokens`, `max_steps`, `max_seconds`, `context_tokens`,
  `temperature`: optional limits of the run. They replace the limits of
  the catalog ("Limits of a run").
- `review_model`: the catalog model of the reviews of the run. Without it,
  the reviews use the review model of the runner. Name the model of the
  run here when its host must load it: a different review model on the
  same host makes the host change its engine for each review.

The CLI has a flag for it: `magi start -model '{"name": "glm-5.3-coder"}'
INSTRUCTIONS`. The apprentice sends the name to the proxy in the model
field of the request. The proxy then selects only that model. A person who
is not an operator gets `rpc.Denied`.

## Limits of a run

The limits of a code change come from the catalog model that does the
work. Two attributes of a model set them:

| Attribute | Limit |
| --- | --- |
| `context` | The tokens of the context window of the engine (`-c` of llama-server). The apprentice compacts its conversation at 91.5 % of it. |
| `work_seconds` | The time limit of one work round. Without it, the round has 3600 seconds. |

The apprentice asks Magi before a run: `GET /runner/v1/models/<name>`,
where the name is `coder` or the model that the run names. Magi answers
with the attributes of the first model of the chain of that name. A model
with no such attribute gives no such limit, and the apprentice keeps the
setting of the runner. Give each local model its `context`: a compaction
limit above the window of the engine makes each long call fail one time
before the apprentice recovers.

## Models that a host must load

A host with more than one engine runs one of them, and refuses a model of
a different engine (`primitives/inference/README.md`, "More than one
engine"). The model proxy tells the host to load the model when the
refused model is the first model of the chain: the model that the run
names, or the model that the task prefers. It waits until the engine is
ready (5 minutes at most), and sends the call again. The host keeps the
model for 20 minutes after its last call, which is longer than the checks
of a code change. Then the host goes back to its first engine with no
action of Magi.

A later model of a chain gets no load: a fallback must not take the GPU
from the model that the host runs. A persona turn loads nothing. While a
host runs a different engine, a turn uses the next model of its chain.

## Backup model

A code change can have a backup model: a model that is slower and more
careful than the model of `step.code-change.work`. Set it with the default
of the task `step.code-change.backup` in the catalog file, and apply the
file (`tools/models.py apply`):

```json
{"task": "step.code-change.backup", "selector": {"name": "qwen3.8-27b-gsq"}}
```

When the usual model cannot finish a change, the `backup` step of the
workflow starts one work round with the backup model (`magi/README.md`,
"Code-change workflow"):

- The checks failed three times, or a guard stopped the apprentice: a
  loop, no progress, a stall, or a stop of the supervisor.
- The work of the first model stays in the environment, with the output
  of its last failed check. The backup model starts from there.
- The run then names the backup model for its work and its reviews, and
  the checks get three new attempts.
- Each run gets one backup round. After it, the task stops or waits for a
  person, as with no backup model.
- A run with a model that an operator named gets no backup round. An eval
  run gets none.
- A gone endpoint or gone money is no cause for a backup round: the chain
  of the work task has the next endpoint.

Give the backup model the attributes `context` and `work_seconds`, and no
tag that puts it in the chain of a different task. When its host must load
it, the load takes the GPU from the usual model for the time of the round
and 20 minutes after it.

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

## Candidates measured on 2026-11-20

These numbers are from one machine with an RTX 4080 (16 GB, about 12 GB
free). The public benchmark numbers of the candidates are not our
results: SWE-bench depends on the harness around the model.

| Model | File | On a 16 GB card | Result |
| --- | --- | --- | --- |
| Gemma 4 26B A4B | UD-Q4_K_M, 16.9 GB | Runs with the experts of 16 layers on the processor: 33 tokens/s, 1028 prompt tokens/s | Turn eval `core-v2`: 42 of 46, against 57 of 57 (`core-v1`) and 12 of 12 (event cases) for gemma-4-12b. Slower and not better for persona turns |
| Devstral Small 2 24B | Q4_K_M, 14.3 GB | Does not fit. With 28 layers on the card: 4 tokens/s | Not usable on 16 GB. Not measured for quality |
| Qwen3.6-27B | Q4_K_M, 16.8 GB | Does not fit (a dense model) | Not measured |

A dense model of 24B to 27B needs a card with 24 GB.

On an RTX 3090 (24 GB), each model fits with 64k context and a KV cache of
8 bits:

| Model | Memory | Tokens/s | Prompt tokens/s | Turn eval `core-v2`, 3 runs | Coding eval, 3 cases |
| --- | --- | --- | --- | --- | --- |
| Qwen3.6-35B-A3B with MTP (in use) | | about 200 | | | 7 defects, 105 minutes |
| Qwen3.6-27B | 18.5 GB | 41 | 1417 | 68 of 69 | 7 defects, 212 minutes |
| Gemma 4 26B A4B | 18.2 GB | 107 | 4137 | 62 of 69 | No change reached the review |
| Devstral Small 2 24B | 19.0 GB | 47 | 1718 | 33 of 69 with its own chat template | 2 of 3 cases: 11 defects, one reject |

Decision (2026-11-21): Qwen3.6-35B-A3B stays the coding model and the review model.
Qwen3.6-27B has the same quality at 2 times the time, so it is the
second choice. Gemma 4 26B A4B and Devstral Small 2 are not usable for code
changes in this harness. For persona turns, gemma-4-12b stays: 69 of 69
on the same set. The details are in `evals/coding/README.md` and
`evals/turn/README.md`.

## Candidates measured on 2026-11-26

Two dense 27B models with a low bit count, against the model in use, on
the same RTX 3090: llama.cpp b12020, MTP with 3 draft tokens, a KV cache
of 8 bits, and 64k context for the tests.

| | Ternary Bonsai 2.5 27B (`Q2_0-mtp`) | Qwen3.8 27B GSQ (`IQ3_XXS-mtp`) | Qwen3.6-35B-A3B (`Q4_K_M`, in use) |
| --- | --- | --- | --- |
| File | 7.9 GB | 10.4 GB | |
| GPU memory at 32k / 64k / 128k / 262k context | 9.8 / 11.2 / 14.0 / 19.6 GB | 12.2 / 13.6 / 16.4 / 22.0 GB | 64k: 22.8 GB, 128k: 23.9 GB |
| Prompt tokens/s | 1320 | 1215 | 3265 |
| Tokens/s | 79 to 92 | 66 to 77 | about 200 |
| Turn eval `core-v3`, 3 runs (90) | 89 | 90 | 88 |
| Turn eval, median seconds for each case | 7.7 | 8.5 | 2.3 |
| Coding eval pr2, pr3, pr7, 2 rounds: tasks that pass | 6 of 6 | 6 of 6 | 5 of 6 |
| Tasks that pass at the first check | 6 | 6 | 3 |
| Probes | 66 of 66 | 66 of 66 | 54 of 66 |
| Minutes for each task | 61 to 73 | 57 to 77 | 29 to 69 |
| Rounds with a complete self-review | 3 of 6 | 6 of 6 | 5 of 6 |

With a KV cache of 16 bits, 262k context does not fit for the 27B models.
The other Bonsai files (`PTQ1_0`, `PQ2_0`) need the llama.cpp of their
maker and were not measured.

Findings:

- Both 27B models make each change pass, with each probe. The model in
  use lost one task to the loop guard (five times the same `edit_file`
  with the same old and new text), and it missed 2 probes in the round
  where each task passed.
- `verdict.py` gives "no change" for both: 3.00 and 3.00 against 2.00 and
  2.82. The two rounds of the model in use differ by more than the gain.
- The 27B models are 2.5 to 3 times slower for each token, and they think
  more before each call: 14 to 30 seconds for a model call against 2 to 9.
- The time limit of the work round (3600 seconds) is the limit for the
  slow models, not the context. Bonsai lost its self-review to the time
  limit in 3 of 6 rounds. At 60000 tokens, each model compacted 0 to 2
  times, so a larger window does not change these three tasks.
- Bonsai is 2.4 GB smaller than the GSQ model. The GSQ model needs fewer
  model calls for a task (27 to 57 against 46 to 81).

Decision: Qwen3.6-35B-A3B stays the coding model and the model of the
engineering persona: a persona turn in 2 seconds against 8 seconds. The
Qwen3.8 27B GSQ model is the backup model of a code change, with a work
round of 7200 seconds. The long cases of the coding eval
(`evals/coding/README.md`) measure what its larger context window gives.
