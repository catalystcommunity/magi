# Note-taker benchmark

The note taker reads each message of the person and gives the changes to
the knowledge graph (`magi/compose/notetaker.go`). This benchmark tests it
without an import: each case has the notes that Magi shows, one message,
and the items that a right answer has. A model runs in minutes, and a case
fails for one clear reason.

## Parts

| File | What it is |
| --- | --- |
| `gen.py` | Makes cases (deterministic for a seed). 15 categories. |
| `data/test-*.jsonl` | The test split: seed 1, 25 cases for each category (375). |
| `../../magi/cmd/notes-eval` | Runs the note taker of Magi on a model, with the same prompt, schema, and checks as Magi, and scores the answers. |
| `bench.sh` | Starts `llama-server` with each local model in turn and runs `notes-eval`. |
| `results/` | One file for each model: the answers (`.jsonl`) and the summary (`.txt`). |

Categories: `new-fact`, `change`, `fix`, `end`, `event`, `third-person`
(a fact of another person, which must not change the person's facts),
`other-fact`, `other-update`, `small-talk`, `question`, `request` (a
calendar or task request, which is not a fact), `plan`, `reports-to-me`,
`birthday` ("I turned 37"), and `multi-fact`.

## Run

```sh
python3 -I evals/notetaker/gen.py evals/notetaker/data --n 25 --seed 1 --split test
go build -o notes-eval ./magi/cmd/notes-eval
./notes-eval -url http://127.0.0.1:18190/v1 -model NAME -label NAME \
  -out evals/notetaker/results/NAME.jsonl
LLAMA=llama-server NOTES_EVAL=./notes-eval evals/notetaker/bench.sh \
  qwen3.5-9b=Qwen3.5-9B-Q4_K_M.gguf gemma-4-e4b=gemma-4-E4B-it-Q4_K_M.gguf
```

A case passes when the answer has each expected item, has no forbidden
item, and (when `allow_extra` is false) has nothing more. Names match by
containment ("Shaw" matches "Dr. Ellery Shaw"), and a relation matches one
name of a list. The summary gives the score with the checks of Magi (what
Magi applies) and the raw score (the answer of the model).

## Rules

- Do not train on the test split. Use other seeds for training data.
- When you change the note taker or the checks, run the benchmark again on
  the models that you use, and keep the results.
