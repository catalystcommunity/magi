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
| `world-v1/`, `world-v2/` | The output of `world.py` for versions 1 and 2 (`world.py OUT 2`). In v2 the other messages follow the dates, and some are near misses (facts of other people, plans that did not happen). The calendar and Longhouse data are the same. |
| `questions-v1.json`, `questions-v2.json` | Questions, actions (Magi changes a calendar or Longhouse), and checks |
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

Version 2 scores with regular expressions on the reply (`re_all`, one of
each group of `re_any`, no match of `re_none`). A reply with a word of
`refusal_words` fails an answerable question. An action has `expect`:
after the action, `run.py --extras CMD` runs `magi-world -extras` and
looks for a new event or task whose fields match (for example the start
in UTC, the calendar, and the RRULE).

```sh
python3 -I evals/world/run.py evals/world/questions-v2.json --magi ./magi \
  --token-file token --label gemma \
  --extras 'magi-world -world evals/world/world-v2 ... -extras'
```

Before each run: remove the action results (`magi-world -clean`) and the
questions and replies of the earlier run (they are in the conversation
that the persona reads).

Version 1: a reply passes when it has every word of `all`, one word of each group of
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

| Run | Score (automatic) | Score (review) | Notes |
| --- | --- | --- | --- |
| v1 `gemma-base` (2026-11-09, local Sunday 8 November) | 20/28, checks 3/3 | about 13 right, 6 partly, 9 wrong; w01 wrong | Busy signal on every question. Import: 35/42 key messages recorded, 20/286 noise messages recorded (as false facts). See the `.review.md` file. |

| v2 `gemma` (2026-11-09) | 39/51, checks 4/4 | 40.5/51 | Import 1 (gemma note taker): wrong-subject facts (the person "works at Orbital Freight"), Nadia's manager "data team", birthday "37". All-day events showed one day early. |
| v2 `glm` (2026-11-09) | 44/51, checks 3/4 | 41/51 | Same knowledge as v2 gemma. 5 replies did not finish (token budget 1024; now 4096 with a retry). It repeats the faults of the graph. |
| v3 `gemma-imp2` (2026-11-09) | 64/76, checks 4/4 | | Import 2 (gemma note taker, with guards). Fails come from the graph: works at "engineering lead", lives on Pine Street, Porto one day early. |
| v3 `glm-imp2` (2026-11-09) | 70/76, checks 3/4 | | Same knowledge. Better at dates, counts, and calendar with tasks. w04: said "Done" with no create_event (guard fixed). |
| v3 `glm-imp3` (2026-11-09) | 71/76, checks 4/4 | | Import 3 (glm-5.3 note taker). Fails: birth year not recorded (2, fixed after), comments not shown (fixed after), a reply that was only a page link, a stray test task. |
| v3 `gemma-imp3` (2026-11-09) | 63/76, checks 4/4 | | Import 3. The good graph does not help a small persona: ages, counts, "this Saturday" dates, and calendar with tasks are wrong. |

### Model comparison (2026-11-09)

| Note taker | Persona | v3 questions | Actions | Persona median / p90 |
| --- | --- | --- | --- | --- |
| gemma-4-12b (local) | gemma-4-12b | 64/76 | 4/4 | 7.7 s / 16.4 s |
| gemma-4-12b (local) | glm-5.3 (Zen) | 70/76 | 3/4 | 10.1 s / 21.8 s |
| glm-5.3 (Zen) | glm-5.3 | **71/76** | **4/4** | 9.8 s / 19.2 s |
| glm-5.3 (Zen) | gemma-4-12b | 63/76 | 4/4 | 6.3 s / 12.8 s |

- The persona model decides most of the score: glm-5.3 does the date
  arithmetic, counts, and joins of calendar and tasks that gemma-4-12b
  gets wrong. A better graph does not make gemma-4-12b better.
- The note taker decides whether the graph stays true over the years. The
  glm-5.3 import recorded the move, the vet change, the job and the
  employer apart, the U12 team, the sale of the car (ended, with an
  event), and Nadia's move (ended). The gemma imports lost the move, mixed
  job and employer, and made facts of other people the person's. The score
  difference is small (70 to 71) because the questions read around the
  faults, but the faults stay in the graph and grow.
- glm-5.3 is slow as a note taker (about 17 s a message, against 5 s) and
  can pass the gateway time limit; the import is now idempotent and the
  CLI tries again. Notes are background work, so the time is acceptable.
- Recommendation: glm-5.3 first for the persona and the note taker, with
  the local hosts as the fallback (`PERSONA_PROVIDER_FIRST=true`,
  `LIBRARIAN_PROVIDER=true`). Keep the local hosts first only when the
  provider must not see the messages.

The scorer of v3 gives the v2 glm replies 41/51, the same as the review.
The review of v2 is `results/20261109-world-v2-gemma-glm.review.md`.

Findings of the baseline (fixed in the code after it, not yet measured):

- "We sold the Mazda" was a retract: the history lost the car. New note
  operation `end` keeps a past fact. One-time happenings are `event`
  items (episodes with the date), not facts.
- Dates in the context were UTC (an evening message showed the next day).
  `when()` now uses the household zone; facts show "since <date>".
- catalystcal search matches one substring; `find_events` now searches
  each word and ranks. The turn context has the calendar events and the
  Longhouse tasks that the words of the message find.
- "This Saturday" on a Sunday gave the past Saturday. The prompt lists the
  dates of the next days, and `create_event` refuses a past time.

Known defects of v1 itself: relative dates ("tomorrow", "this week")
depend on the day of the run; action checks ask the same model; abstaining
on an answerable question can pass; the noise does not follow the dates
(climbing after the injury). v2 fixes these.
