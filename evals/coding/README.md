# Coding eval

The coding eval measures a coding model on real tasks of this repository.
Each case is a code change that Magi made, that a reviewer approved, and
that is merged. The eval gives the same instructions to a candidate model
at the same base commit, and compares the result with the approved change.

The eval grows: each approved and merged code change can become a case.

## Cases

`cases.jsonl` has one case on each line. `cases/<id>.md` has the
instructions of the case: the text that the operator gave to Magi for the
first attempt.

| Field | Meaning |
| --- | --- |
| `id` | `pr<number>`: the pull request of the approved change |
| `title` | The title of the task |
| `run` | The workflow run that made the approved change |
| `base` | The commit where the task started |
| `approved` | The last commit of the approved change |
| `changed` | The files that the approved change touched |
| `hidden_tests` | The test files of the approved change |

To add a case, use the pull request of an approved code change of Magi.
The instructions are the first instructions of the task, not the review
comments: the eval measures how good the first attempt is.

## Scores

1. **Check.** `./tools/check.sh` passes with the change of the candidate.
2. **Review.** A judge model grades the diff of the candidate against the
   instructions, the approved diff, and the review lessons
   (`magi/compose/baseknowledge/review-lessons.md`): approve, small
   comments, or reject, with the reasons. A person reads a sample of the
   grades.
3. **Hidden tests.** The test files of the approved change, put on top of
   the change of the candidate. This score is weak: the instructions tell
   the behavior, not the names, so a correct change can fail a hidden test
   that uses other names. Use it only together with the other two scores.

Also record the model calls, the time, and the cost of each case.

## Run

`run.py` runs the cases through the code-change workflow of Magi, one case
after the other, and collects the results. It needs a token of an operator
and the model of the candidate:

```sh
python3 evals/coding/run.py --url URL --token-file F \
  --model '{"name": "qwen3.6-35b-a3b-studiodt", "context_tokens": 60000}' \
  --label qwen3.6-35b-a3b
```

`--model` is the JSON object of the `model` field of `StartTask`
(`docs/models.md`). `--label` names the candidate in the output path.
`--cases pr2,pr5` runs a part of the cases, and `--max-minutes` sets the
time limit of one case (the default is 240).

The script starts each case with kind `code-change`, repo `magi`, the
title of the case, the instructions of `cases/<id>.md`, the base of the
case, and the model. It polls the task every 60 s until the task waits for
a decision, or ends, or the time limit passes. It does not decide: a
person grades the run and rejects it. A 401 answer stops the script: the
token expired. An other error of one poll is logged and the poll repeats;
five errors in a row stop that case.

Each case writes `results/<label>/<id>/`: `result.json` (the run, the
status, the step of the end, the check outcome with the attempts, the
apprentice steps, tool calls, and cost, the minutes, and the files that
the diff touches together with the `changed` files of the case),
`diff.patch`, and `check.output.txt`. The script prints one line for each
case at the end. Keep the results in the repository, as the other evals
do.

Grade each run, then close it:

```sh
magi decide RUN reject
```

### One case by hand

The candidate runs in the code-change workflow of Magi (a sandbox on a
runner). Start the case with the model of the candidate:

```
magi start -repo magi -title "<title of the case>" -base <base of the case> -model '{"name": "<candidate model>"}' "$(cat cases/<id>.md)"
```

The `-model` flag needs an operator. The name is a model of the catalog
(`docs/models.md`); the proxy of Magi sends the model calls of the
apprentice to it. Without the flag, the task default of
`step.code-change.work` selects the model. Do not run a candidate with
`magi_apprentice run --local`: that mode has no isolation.

The `-base` flag needs an operator too. It is the commit of the default
branch where the change of the case started. A run with a base is an eval
run: it starts at that commit, it ends at review, and it never merges or
publishes. Magi refuses the decision approve on such a run. Grade the run,
then close it:

```
magi decide RUN reject
```
