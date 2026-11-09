# World evaluation

This evaluation tests Magi as a person uses it after years of use: the
knowledge graph that the note taker made from three years of messages, the
person's calendars (catalystcal), and the household projects and tasks
(Longhouse). The questions are the questions that a person asks. Most of
them need more than one look up.

All data is synthetic. The person is "Jamie" (the test user); the partner
Alex is the second test user.

## Parts

| File | What it is |
| --- | --- |
| `world.py` | Makes the world (fixed seed): `history.json`, `calendar.json`, `longhouse.json` |
| `world-v1/` | The output of `world.py` for version 1 |
| `questions-v1.json` | Questions, actions (Magi changes a calendar or Longhouse), and checks |
| `run.py` | Asks the questions with the `magi` command and polls `magi activity` |
| `results/` | One file for each run |

The history has 42 key messages (facts and changes of facts, from January
2024 to October 2026) and about 290 other messages (small talk, questions,
days). The other messages do not change a fact.

## Put the world in place

1. The person and Alex connect their calendars to Magi (`magi connect
   calendar`), and the person connects Longhouse (`magi connect
   longhouse`).
2. In the catalystcal web app: the person makes the calendars "Work" and
   "Family" and shares "Family" with Alex (write). Alex calls the default
   calendar "Alex" and shares it with the person (write). Magi's grant does
   not have `calendars.manage`, so Magi cannot do this.
3. Put the events, projects, and tasks in place:

   ```sh
   magi-world -world evals/world/world-v1 -identity https://magi.example.local \
     -token-file token-magi -me user:<uuid>@example.local -alex user:<uuid>@example.local \
     -calendar https://cal.example.local -longhouse https://longhouse.example.local
   ```

4. Import the history as the person (about 5 seconds a message):

   ```sh
   magi import-history -persona assistant -json evals/world/world-v1/history.json
   ```

## Run

```sh
python3 -I evals/world/run.py evals/world/questions-v1.json --magi ./magi \
  --server https://magi.example.local --token-file token --label try-1
```

A reply passes when it has every word of `all`, one word of each group of
`any`, and no word of `none` (a word of `none_unless` is permitted next to
one of its excuse words). An `abstain` question also needs a word that says
that Magi does not know. `busy_seen` counts the questions in which the
activity showed while the reply was made.

## Rules

- Do not change a set after a run used it. Make a new version.
- The actions change the calendars and Longhouse. Run them once for each
  world, or remove their results before the next run.
- An antagonistic review reads each result file (the replies, not the
  scores) and reports wrong passes and wrong failures.

## Results

See the table at the end of this file.
