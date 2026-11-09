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

## Results (2026-11-09, test split seed 1, 375 cases)

| Model | With checks | Raw | Median |
| --- | --- | --- | --- |
| gemma-4-12b Q4_K_M (local) | 93% | 77% | 2.7 s |
| qwen3.8-27b IQ3_XXS (local) | 83% | 77% | 1.9 s |
| qwen3.6-35b-a3b Q4_K_M (local, MoE on CPU) | 80% | 70% | 6.0 s |
| gemma-4-e4b Q4_K_M (local) | 74% | 61% | 1.4 s |
| qwen3.5-9b Q4_K_M (local) | 66% | 62% | 1.5 s |
| qwen3.5-4b Q4_K_M (local) | 55% | 53% | 1.1 s |
| gpt-oss-20b MXFP4 (local) | 51% | 42% | 0.9 s |
| glm-5.3 (Zen, reference) | 90% (24 calls failed; 97% of the others) | 83% | 5.6 s |

Local models ran on an RTX 4080 (16 GB) with llama.cpp b12020, two slots,
no reasoning (`--reasoning-budget 0`). gemma-4-12b is the best local model.
Its weak categories are `other-update`, `third-person`, and
`reports-to-me`. The checks of Magi add 16 points to it.

## Training

The training data are cases of `gen.py` with other seeds than the test
split and `--train` (the last phrasing of each list is held out). They are
in `train/` as cases, not as chat records, because the chat records repeat
the prompt (5 KB each); make the records again with `notes-eval -export`,
so that they always have the current prompt of Magi.

```sh
python3 -I evals/notetaker/gen.py evals/notetaker/train --n 150 --seed 1001 --split cases-a --train
python3 -I evals/notetaker/gen.py evals/notetaker/train --n 200 --seed 1002 --split cases-b --train \
  --only other-update,third-person,event,reports-to-me,birthday,other-near-self
./notes-eval -cases 'evals/notetaker/train/cases-*.jsonl' -oracle   # must be 100%
./notes-eval -cases 'evals/notetaker/train/cases-*.jsonl' -export chat/
uv venv venv --python 3.12 && VIRTUAL_ENV=venv uv pip install torch --index-url https://download.pytorch.org/whl/cu128
VIRTUAL_ENV=venv uv pip install numpy transformers peft trl datasets accelerate bitsandbytes sentencepiece protobuf
venv/bin/python -I evals/notetaker/train_lora.py --base google/gemma-4-12b-it --data 'chat/train-*.jsonl' --out lora/
```

Some messages of the no-op categories (small talk, questions, requests,
plans) and a part of `event` and `birthday` are the same text in the
training data and the test split; the notes and the ids are not. Report
the score of a trained model on all test cases and on the test cases whose
text is not in the training data.
