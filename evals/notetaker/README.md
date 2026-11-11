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
| `data/test-*.jsonl` | The test split: seed 1, 25 cases for each category (400). |
| `../../magi/cmd/notes-eval` | Runs the note taker of Magi on a model, with the same prompt, schema, and checks as Magi, and scores the answers. |
| `bench.sh` | Starts `llama-server` with each local model in turn and runs `notes-eval`. |
| `heldout.py` | Scores results only on the test cases whose text is not in `train/`. |
| `train_lora.py` | Trains a LoRA adapter (QLoRA, 4 bits) on the chat records of `train/`. |
| `results/` | One file for each model: the answers (`.jsonl`) and the summary (`.txt`). |

Categories (16): `new-fact`, `change`, `fix`, `end`, `event`, `third-person`
(a fact of another person, which must not change the person's facts),
`other-fact`, `other-update`, `small-talk`, `question`, `request` (a
calendar or task request, which is not a fact), `plan`, `reports-to-me`,
`birthday` ("I turned 37"), `multi-fact`, and `other-near-self`.

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

To convert and benchmark an adapter (run the converter from its directory,
because it imports its own modules):

```sh
cd llama.cpp && venv/bin/python convert_lora_to_gguf.py --base BASE_DIR --outtype f16 --outfile lora.gguf lora/
LLAMA=llama-server NOTES_EVAL=./notes-eval evals/notetaker/bench.sh \
  gemma-4-12b-lora=gemma-4-12B-it-Q4_K_M.gguf:--lora,lora.gguf
python3 -I evals/notetaker/heldout.py evals/notetaker/results/gemma-4-12b{,-lora}.jsonl
```

### Training results (2026-11-09, lora-v1)

Gemma 4 12B, QLoRA nf4, rank 16 on all projections of the language model,
learning rate 1e-4, one epoch over the 3,600 cases (225 steps of 16), 2 h
on the RTX 4080 (13.7 GB). The loss was 5.7 at step 5 and 0.002 at step 60.
The adapter (f16 GGUF, 131 MB) ran on the Q4_K_M base. 200 of the 400 test
cases have a text that is not in the training data ("held out").

| Model | All 400 | Held out (200) | Raw | Median |
| --- | --- | --- | --- | --- |
| gemma-4-12b, run 1 | 354 (88%) | 174 (87%) | 309 | 2.6 s |
| gemma-4-12b, run 2 | 343 (86%) | 165 (82%) | 305 | 3.1 s |
| lora-v1 (step 225) | 372 (93%) | 177 (88%) | 367 | 1.8 s |
| lora-v1 at scale 0.5 | 352 (88%) | 165 (82%) | 338 | 2.4 s |
| lora-v1 step 100 | 367 (92%) | 174 (87%) | 359 | 1.8 s |

The two baseline runs show the noise: about 5 cases on the held-out set.

On the held-out set, lora-v1 is better in `birthday` (0/8 to 7/8),
`third-person` (12-19/25 to 25/25), `other-update`, and `reports-to-me`. It
is worse in `end` (7/7 to 1/7), `event` (6/6 to 2/6), and `new-fact` (17/18
to 14/18). These failures are the phrasings that `--train` holds out
("X moved to the payments team; X doesn't report to me anymore", "We got
back from a week in Porto", "Dr. X at Y Clinic"). On a new phrasing the
adapter does not give the id of the claim, drops the event, or writes text
into a JSON string ("(Wait, I need to check the rules again)"). Thus the
adapter learned the phrasings of `gen.py`, not the task.

Do not deploy lora-v1. Before the next training run:

1. Add more phrasings to each list of `gen.py` (the loss was 0 after 60
   steps, so the data have too little variety).
2. Train for fewer steps or with a lower learning rate, and keep a
   checkpoint for each 25 steps.
3. Compare only on the held-out set, and run the baseline two times.


## Version 2 (2026-11-10)

| File | What it is |
| --- | --- |
| `gen2.py` | Makes cases with graph checks, 22 categories. Each phrasing list, name pool, and style has three parts (index mod 5: 0-2 train, 3 dev, 4 test), so the test split has no wording of the training data. |
| `data2/` | test (46 cases for each category, 1,012) and dev (220). |
| `train2-labeled/` | 4,197 training cases with the answer of a teacher (gemma passes, then GLM for the misses). |
| `paraphrase.py` | A model writes each case again in other words and styles. The notes and the checks stay; a rewrite must keep each name, word, and number that the checks look for. |
| `data3/` | GLM rewrites of `data2/`: test (893) and dev (148, incomplete). |
| `results/v2/` | The summaries of the runs below. |

Make the cases:

```sh
python3 -I evals/notetaker/gen2.py evals/notetaker/data2 --part test --n 46 --seed 7
python3 -I evals/notetaker/gen2.py evals/notetaker/data2 --part dev --n 10 --seed 8
python3 -I evals/notetaker/gen2.py evals/notetaker/train2 --part train --n 200 --seed 9 --per-file 400
```

A graph check applies the items to a graph of the notes
(`magi/compose/notesim.go`, as `applyNotes` and the knowledge store do)
and examines the result: a fact exists, a fact stays, a fact ended, an
event exists, or no new entity. Thus each right form of an answer passes.

### Prompt of Gemma 4

With only `--reasoning-budget 0`, llama-server puts `<|think|>` in the
system turn of Gemma 4. Training with the HF template has no `<|think|>`
and has an empty thought channel. Use
`--chat-template-kwargs '{"enable_thinking":false}'` (`bench.sh` and
`hostsetup` do this now). Without it, lora-v2 merges facts into one item.

### Results

All runs use Gemma 4 12B Q4_K_M on the RTX 4080. "data2" is the template
test split; "data3" is the GLM rewrites, with wording that no model was
trained on.

| Model | data2 test (1,012) | data2 dev (220) | data3 test (893) |
| --- | --- | --- | --- |
| GLM 5.3 (runner `coder`) | 940 (93%) | - | - |
| gemma, with `<\|think\|>` | 857 (85%) | 172 (78%) | - |
| gemma | 894 (88%) | 178 (81%) | 769 (86%) |
| lora-v2 checkpoint-75, with `<\|think\|>` | - | 153 (70%) | - |
| lora-v2 checkpoint-75 | 915 (90%) | 184 (84%) | 802 (90%) |

lora-v2: QLoRA nf4, rank 16, learning rate 1e-4, one epoch over 4,197
cases (263 steps of 16), a checkpoint each 25 steps. The loss stayed near
0.04 (lora-v1: 0.002). The dev split chose checkpoint-75 (184); the last
checkpoint had 183.

The guard fixes of 2026-11-10 (birthday age with "today", the self label
in reports-to-me, more words of a change) give gemma 927 (92%) and
checkpoint-75 940 (93%) on the data2 test. They were made from data2 test
failures and give 0 on dev, so do not use these numbers to compare.

On data3, checkpoint-75 is better in `birthday` (19 to 41 of 44), `kin` (23
to 36 of 44), and `plan` (30 to 45 of 45). It is worse in `third-person` (33
to 22 of 34) and `multi-fact` (32 to 27 of 44): for a new phrasing it often
gives no items. There are only 6 training phrasings for each category.

## Version 3 (2026-11-11)

| File | What it is |
| --- | --- |
| `data3/` | GLM rewrites of `data2/`: test (893) and dev (381). |
| `train3/` | Gemma rewrites of `train2/` (8,152; 2 for each case). Train and test rewrites come from different models. |
| `train3-labeled/` | 10,024 training cases with a teacher answer: the v2 labels and the labeled rewrites, with half of the cases that have no items removed. |
| `results/v3/` | The summaries of the runs below. |

Labels of the rewrites: lora-v2 checkpoint-75 first (6,786 of 8,152
pass), then gemma on its misses (472 of 1,309), then GLM on the rest (275
of 830). An answer is a label only when its graph checks pass. The 555
rewrites with no passing answer are not in the training data.

lora-v3: the same settings as lora-v2, one epoch over 10,024 cases (627
steps of 16), a checkpoint each 50 steps, 5.4 h. The dev split chose
checkpoint-550.

| Model | data3 test (893) | data3 dev (381) | data2 test (1,012) |
| --- | --- | --- | --- |
| gemma | 769 (86%) | - | 894 (88%) |
| lora-v2 checkpoint-75 | 802 (90%) | 326 (86%) | 915 (90%) |
| lora-v3 checkpoint-550 | 837 (94%) | 342 (90%) | 982 (97%) |

The data2 test score of lora-v3 has the guard fixes of 2026-11-10 (gemma
with them: 92%). On data3, lora-v3 is better than gemma in `multi-fact`
(32 to 41 of 44), `kin` (23 to 39), `birthday` (19 to 42), `alias`,
`pet-ref`, `plan`, and `same-name`. It is worse in `third-person` (33 to
29 of 34), `new-fact` (39 to 35 of 44), `reports-to-me` (29 to 26 of 36),
and `end` (41 to 38 of 42).

The adapter is not in the repository (131 MB). On the RTX 4080 host it is
`~/models/notetaker-lora-v3-gemma-4-12b.gguf`. To make it again: train with
`train_lora.py` on the `-export` of `train3-labeled/`.

### Next

1. Serve the adapter only for the note taker: llama-server
   `--lora FILE --lora-init-without-apply`, and the note-taker request sets
   the scale of the adapter (`"lora": [{"id": 0, "scale": 1}]`). Persona
   chat stays on the base model.
2. Make rewrites of the weak categories (`third-person`, `reports-to-me`,
   `new-fact`) and train again.
