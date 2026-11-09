# Plans evaluations

These sets test the to-dos, reminders, and plans of a persona
(`docs/plans.md`), through the `magi` command, as a person uses it.

A set has:

- `persona`: the persona that gets each message (`persona:assistant`).
- `steps`: one message each. After the reply, the runner reads
  `magi todos -all -json` and checks:
  - `todos`: to-dos that must exist, with title words, `kind`, `state`,
    `plan`, `due`, `remind`, and `after` (the key of another to-do). A
    later step names a to-do by its `key`. `new: true` must be a new
    record (a change makes a new run).
  - `absent`: title words that no to-do may have.
  - `once`: title words that exactly one open to-do must have.
  - `reply_all`, `reply_none`: words that the reply must have, or must not
    have.

  The runner waits up to 30 seconds for the to-dos, because the workflow
  steps run after the reply.

Times are relative to the day of the run, in the zone of the household
(`--tz`, the same as `MAGI_TIME_ZONE`): `+1 09:00` is tomorrow at 09:00,
`fri` is the date of the next Friday after today, and `now+120m` is 120
minutes after the message, with 5 minutes of tolerance.

## Rules

- Do not change a set after a run used it. Make a new version.
- Use a new subject for each run.
- Use only synthetic data.

## Run

```sh
identity mint --key issuer.json --subject user:plans-eval-1@example.local --ttl 6h --out token
python3 -I evals/plans/run.py evals/plans/round0.json --magi ./magi \
  --server https://magi.example.local --token-file token --tz America/Denver --label try-1
```

The runner writes `results/<time>-<version>-<label>.json` and prints a
summary with the time of each step (median, p90, maximum).

## Results

| Run | Steps | Median ms | p90 ms | Notes |
| --- | --- | --- | --- | --- |
| round0 `gemma-a` (2026-11-08) | 10/13 | 9485 | 23442 | p05 passed in fact (a runner error with `after` in one step, now fixed). p08 "next Tuesday" is not clear (the model took 17 November); round1 says "Tuesday". p09: no tool call, because the model did not have the id; the open to-dos are now in the context of each turn. |
| round1 `gemma-r1a` (2026-11-08) | 13/14 | 13044 | 24834 | p14: `change_todo` with `due` on a reminder; a due time of a reminder is now its time. Two runs at the same time. |
| round1 `gemma-r1b` (2026-11-08) | 14/14 | 11093 | 33812 | |

All runs used gemma-4-12b on the RTX 3090.
