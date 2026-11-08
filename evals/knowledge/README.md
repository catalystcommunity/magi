# Knowledge evaluations

These sets test what a person gets from Magi's knowledge, through the
`magi` command, as a person uses it. See `docs/knowledge.md`.

A set has:

- `seed`: facts that the person says to `tell_persona`.
- `questions`: questions to `ask_persona`, another persona. The answer
  must come from the knowledge store, because the other persona does not
  have the conversation of the first one.
- `changes`: a fix, an update, or a retraction said to `tell_persona`, and
  questions that check the result.
- `store_after_seed`, `store_after_changes`: words that `magi know` must
  show (`all`) or must not show (`none`).

A reply passes when it has all the words of `all` and none of `none`. An
`abstain` question also needs words that say that Magi does not know.

## Rules

- Do not change a set after a run used it. Make a new version.
- Use a new subject for each run. The changes modify the data of the run.
- Use only synthetic data.

## Run

```sh
identity mint --key issuer.json --subject user:kg-eval-1@example.local --ttl 24h --out token
python3 -I evals/knowledge/run.py evals/knowledge/round0.json --magi ./magi \
  --server https://magi.example.local --token-file token --label try-1
```

The runner writes `results/<time>-<version>-<label>.json` with each reply,
its model, and its time, and prints a summary.

## Results

| Run | Store after seed (missing) | Questions | Changes | Store after changes |
| --- | --- | --- | --- | --- |
| round0-v2, first schema, run c | 3 of 16 | 3/13 + 3/3 abstain | 0/5 | 4 missing, 2 present that must be gone |
| round0-v2, first schema, run d | 8 of 16 | 2/13 + 3/3 abstain | 1/5 | 4 missing, 1 present that must be gone |
| round0-v2, new schema, run r1a | 12 of 16 | 1/13 + 3/3 abstain | 0/5 | 1 missing |
| round0-v2, new schema, run r1b | 7 of 16 | 1/13 + 3/3 abstain | 1/5 | 1 missing |
| round0-v2, new schema + records in context, run r3a | 5 of 16 | 9/13 + 3/3 abstain | 2/5 | 2 missing |
| round0-v2, new schema + records in context, run r3b | 2 of 16 | 10/13 + 2/3 abstain | 3/5 | 2 missing, 1 present that must be gone |

All runs used the local model qwen3.5-9b.

In the first schema, the persona said "I've recorded" and "I've updated"
when it did not call a tool. A question to the second persona got invented
phone numbers and addresses. Version 1 of the set asked the questions in
the same conversation; the persona answered from the conversation, and the
results did not measure the knowledge store. Version 1 is kept for that
reason.

In the new schema the persona did not invent an answer in any run. In r1
the second persona did not call `recall` (the turn log has no calls), so
Magi now puts the matching records into each turn ("records in context").
The remaining failures are mostly facts that the model said it recorded or
changed, with no tool call. The `update` of the dentist reached the store in
r1a, r1b, and r3a.

The substring check has known errors: "google" passes "It is likely in
Google Calendar", which is a guess. a03 in r3b fails because the reply
names Tucson ("the records show that Dana lives in Tucson") while it says
that it does not know.

### Local model comparison (round0-v2, 2026-11-06)

Two runs of each model, at the same time, on one RTX 4080 (16 GB). Each run
has a new subject. "Questions" counts the 16 questions, with the 3 abstain
questions. "Checks" counts the 5 checks after the changes. The note taker
(`MAGI_NOTE_TAKER`, see `magi/README.md`) uses the same local model.

| Model | Note taker | Questions | Checks | Median turn | Longest turn |
| --- | --- | --- | --- | --- | --- |
| qwen3.5-9b | on | 10, 6 | 2, 1 | 4 s | 9 s |
| qwen3.5-9b | off | 5, 5 | 1, 1 | 2 s | 7 s |
| gemma-4-12b | on | 14, 13 | 4, 5 | 7 s | 66 s |
| gemma-4-12b | off | 10, 10 | 3, 4 | 5 s | 94 s |
| gpt-oss-20b | on | 13, 8 | 5, 2 | 5 s | 17 s |
| gpt-oss-20b | off | 12, 9 | 3, 2 | 3 s | 11 s |
| qwen3.6-35b-a3b | on | 13, 13 | 2, 2 | 26 s | 50 s |
| qwen3.6-35b-a3b | off | 9, 11 | 3, 5 | 22 s | 37 s |
| qwen3.8-flash-next (IQ3_XXS) | on | 15, 15 | 5, 4 | 60 s | 146 s |
| qwen3.8-flash-next (IQ3_XXS) | off | stopped | stopped | | over 300 s |
| glm-5.3 (Zen), no local model | on | 10, 12 | 1, 2 | 8 s | 28 s |
| glm-5.3 (Zen), no local model | off | 11, 12 | 3, 3 | 8 s | 33 s |
| gemma-4-12b, default, orphan removal | on | 14, 14 | 4, 4 | 8 s | 84 s |
| gemma-4-12b, default2, records after the notes | on | 14, 14 | 4, 4 | 8 s | 62 s |
| gemma-4-12b, default3, earlier answers marked | on | 12, 14 | 5, 4 | 7 s | 58 s |
| gemma-4-12b, default4, superseded answers replaced | on | 13, 14, 14, 14 | 4, 5, 5, 5 | 8 s | 51 s |
| qwen3.8-flash-next (IQ3_XXS), RTX 3090 (Windows) | on | 14, 15 | 4, 5 | 70 s | 159 s |
| qwen3.8-27b (IQ3_S), RTX 3090 | on | 10, 9 | 2, 1 | 18 s | 69 s |
| qwen3.6-35b-a3b, RTX 3090 | on | 13, 12 | 2, 1 | 7 s | 28 s |
| gemma-4-12b, RTX 3090, KV cache q8_0 | on | 12, 14 | 5, 5 | 12 s | 48 s |
| gemma-4-12b, RTX 3090, KV cache f16 | on | 13, 14 | 5, 5 | 9 s | 35 s |

The files `glm-5.3-nolocal-*` were runs for gpt-oss-20b and qwen3.6-35b-a3b
in which the inference host did not start; all turns used Zen. The runs of
Qwen3.8-Flash-Next with the note taker off stopped at the 300 s limit of the
runner (10/12 and 9/11 passed before the stop).

Findings:

- The note taker helps the small models most (gemma-4-12b +4 questions,
  qwen3.5-9b +3). With the note taker off, the persona must call the tools,
  and these models often say "I've updated" with no call.
- Qwen3.8-Flash-Next gives the best answers, but its turns are 60 s and
  more. It does not fit in 16 GB: part of the model and the n-gram table
  are in system memory.
- In all runs before the orphan removal, "tucson" stayed in the store after
  the fix of Dana's city. The fix removed the claim but not the old place.
  With the removal (`primitives/knowledge`, `fix`), no old value stays.
- The retract c03 ("There is no chores calendar. Forget it.") failed in
  the default runs. The note taker retracted the claim, but the persona got
  the records from before the change and said that it did not find the
  record. Now Magi gives the records after the changes, and the persona
  confirms the retract (runs `default2`). The check c03.1 still fails: the
  store has no chores calendar, but the asking persona repeats its own
  answer to the same question (q08), which is in its history. Now Magi marks
  each earlier answer before a change of the records (runs `default3`).
  c03.1 passed in one of the two runs; in the other, the persona copied its
  q08 answer word for word.
- Now each reply keeps the records that it got. An earlier answer that used
  a record that changed after it is replaced with a note of the change
  (runs `default4`, `magi/README.md`). c03.1 passed in 3 of 4 runs. No run
  named the chores calendar. The failed run named only the sports
  calendar, not the family calendar.
- The CLI printed the new record attachments under each reply, and
  `run.py` scored them as reply text. `run.py` now removes the attachment
  and link lines. Only the `default4` runs changed (rescored): in a03 the
  attachments named Dana, the sister, which made a correct abstain fail.
- In two Gemma runs, the reply started with the template text
  `<|channel>thought<channel|>`. `magi/model` now removes this text.

- RTX 3090 (24 GB, Windows host `ht0-vr1`, through `/inference/connect`):
  Qwen3.8-Flash-Next is not faster than on the 4080. The model is 76 GB,
  thus most of it is in system memory, and the CPU sets the speed.
  Qwen3.8-27B fits in the GPU, but it fails most changes. Qwen3.6-35B-A3B
  is fast, but fails most changes too. Gemma 4 12B gives the same results
  as on the 4080. A KV cache of q8_0 makes it slower on the 3090: use f16.

Gemma 4 12B with the note taker is the default local model. The persona
tries the 3090 host first, then the 4080 host, then Zen.
