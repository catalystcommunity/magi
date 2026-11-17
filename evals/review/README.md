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

## Results: lens review with context, 12 cases, 1 sample (2026-11-17)

| Host | TA | FA | TR | FR | Invalid | Time per case |
| --- | --- | --- | --- | --- | --- | --- |
| Qwen3.6-35B-A3B, RTX 4080, experts partly on CPU | 4 | 3 | 3 | 2 | 2 | 1,025 s |
| Qwen3.6-35B-A3B with MTP, RTX 3090, all on GPU | 4 | 3 | 3 | 2 | 0 | 163 s |

The counts are the same, but the verdict is different on 7 of 12 cases.
Thus one sample is mostly noise. Case 23 is caught both times, with the
defect that the person named. The 3 false approvals stop auto-approval.

### 3 samples (15 lens calls for each case), RTX 3090

| Rule | TA | FA | TR | FR |
| --- | --- | --- | --- | --- |
| Approve only when all 15 calls approve | 3 | 2 | 4 | 3 |
| Also: a defect of the requirements lens is revise | 3 | 1 | 5 | 3 |
| Requirements defect, or 2 revise votes of 15 (not in the code) | 6 | 1 | 5 | 0 |

Time per case: 478 s. In case 27, the requirements lens found the
difference from the task ("the task specifies AdminGetRunRequest, the code
defines AdminGetRunDiffRequest"), called it minor, and approved.
review.py now makes such a sample revise. Case 24 stays a false approval:
the person knew that later views need four more services, and the diff
does not show this.

The 3 false revisions come from one revise vote (or one invalid answer)
in 15 calls. The rule of 2 votes removes them and keeps each true
revision. It was found on these 12 cases, so the code keeps "all approve"
until the other 26 cases confirm it. Auto-approval needs 0 false
approvals. Thus the lens reviewer is a hint for the person now, not a
gate.
