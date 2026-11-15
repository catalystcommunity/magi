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

The candidate runs in the code-change workflow of Magi (a sandbox on a
runner). Start the case with the model of the candidate:

```
magi start -repo magi -title "<title of the case>" -model '{"name": "<candidate model>"}' "$(cat cases/<id>.md)"
```

The `-model` flag needs an operator. The name is a model of the catalog
(`docs/models.md`); the proxy of Magi sends the model calls of the
apprentice to it. Without the flag, the task default of
`step.code-change.work` selects the model. Do not run a candidate with
`magi_apprentice run --local`: that mode has no isolation.
