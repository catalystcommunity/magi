# Reviewer eval

This eval measures the reviewer (`primitives/apprentice/magi_apprentice/review.py`)
against the review decisions of a person. The reviewer reads the task and
the diff of a code change, and says `approve` or `revise`.

## Cases

Each line of the cases file is one decision of a person:

```json
{"id": "07-run_x-1-rev", "task": "<title and instructions>", "patch": "<diff>",
 "label": "approve", "comment": "<what the person wrote>"}
```

The cases come from the review steps of code-change runs of an
installation: the patch artifact of the round, and the decision on it. A
reject of a test run is not a case. A reject of an eval run counts as
`revise`. When a later fix showed a defect in an approved change, the label
is `revise`. When the person sent the change back earlier, the task text
also holds that comment, because the reviewer must check each item of it.
The cases file holds the text of private tasks, so it stays out of the
repository.

## Run

```sh
python3 evals/review/run.py --url https://magi.example.local/runner/v1 \
  --token-file runner.token --model qwen3.6-35b-a3b-studiodt \
  --cases cases.jsonl --label qwen-s3 --samples 3
```

The results go to `evals/review/results/<label>/`: one file for each case
with each answer, and `summary.json`.

## Reading the result

- `false_approvals`: the person sent the change back, and the reviewer
  approved it. Auto-approval needs 0.
- `false_revisions`: the person approved, and the reviewer asked for
  changes. This costs a work round, not a defect.
- `invalid_samples`: answers with no valid JSON verdict. They count as
  `revise`.
