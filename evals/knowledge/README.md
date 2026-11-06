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
