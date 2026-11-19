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

### Check on the other 26 cases, 3 samples, RTX 3090

| Rule | TA | FA | TR | FR |
| --- | --- | --- | --- | --- |
| All approve, and a requirements defect is revise (the code) | 5 | 0 | 10 | 11 |
| Requirements defect, or 2 revise votes of 15 | 10 | 2 | 8 | 6 |

Median time per case: 411 s. The rule of 2 votes approves 2 changes that
the person sent back (cases 28 and 32). Thus the code keeps "all approve".

All 38 cases with the rule of the code: TA 8, FA 1, TR 15, FR 14. The
reviewer approves 8 of the 22 changes that the person approved, and it
misses 1 of the 16 changes that the person sent back (case 24).

10 of the 390 answers had broken JSON, and most of them were approvals.
review.py now reads a raw newline in a string, and asks a lens once more
when its answer is broken. The reasoning-budget message of the server
must not ask for a tool call: a review call has no tools, and the model
then writes a fake tool call instead of the verdict.

Next: look at the 14 false revisions. One false revision costs a work
round, so they decide how useful the gate is.

### The passing checks: in the prompt, or in the harness only (38 cases, 3 samples)

A review step comes after the check step, so the checks passed on each
case. Two ways to use this were tested:

| Way | TA | FA | TR | FR |
| --- | --- | --- | --- | --- |
| No checks (from the table above) | 8 | 1 | 15 | 14 |
| The prompt says that the checks passed, and the harness drops compile and mode claims | 13 | 7 | 9 | 9 |
| The harness drops compile and mode claims, and the prompt does not say it (rescore of the earlier answers) | 10 | 2 | 14 | 12 |

The text in the prompt made the model trust the change. It found fewer
defects of all kinds, not only fewer compile claims: 7 changes that the
person sent back got 15 approvals of 15. Thus the prompt does not say that
the checks passed. The harness uses the checks: review.py verify makes a
compile claim minor when the checks passed, and a file-mode claim minor when
the diff changes no mode.

The 2 false approvals of the harness-only way are case 24 (also missed
without checks) and case 28. In case 28 the reviewer was right before only
because of a false compile claim. The real defect is a database test that
skips in the checks and has a wrong expected order.

### More rules in the lens prompts (38 cases, 3 samples)

The coding eval found defects that the reviewer did not find. Three rules
were added to the lens prompts: words of count, place, and order in the
requirements lens; return values that callers use, and code outside the
error guard, in the logic lens; a test through the entry point in the
tests lens. The harness drops compile and mode claims in both rows.

| Prompts | TA | FA | TR | FR | False approvals |
| --- | --- | --- | --- | --- | --- |
| Earlier prompts (from the table above) | 10 | 2 | 14 | 12 | 24, 28 |
| With the three rules | 12 | 3 | 13 | 10 | 27, 28, 32 |

The rules found case 24, but cases 27 and 32 got 15 approvals of 15. Case
27 copies a request type that the task names, which is the defect that the
requirements rule is for. A false approval costs more than a false
revision, so the prompts did not change. Each case took about 13 minutes on
the RTX 3090.

Case 00 is a question of the label, not of the reviewer. The task says
"Change nothing else", and the change also edits a test file. The person
approved the test fix. By the words of the task, the reviewer is right to
say revise.

### A rule in the harness for case 27

Case 27 is a mechanical defect: the task gives the contract line
`admin-get-run-diff: AdminGetRunRequest -> TaskDiff`, and the change adds
the line with a copy of the request type. A rule finds this each time, so
the diff gate of the apprentice (`diffgate.py`) has it now. When the task
gives a contract operation or type, and the change adds the same name in
a different form, the gate gives a hint once. The model then uses the
form of the task, or gives a reason in the summary.

On the 38 cases, the rule finds case 27. It also gives a hint on case 31
(approved): the change adds a field that the task does not ask for. A
field that another step of the task adds is not a hit (case 28).

### The requirements lens in steps (38 cases, 3 samples)

The requirements lens was written again in the form of steps: copy each
sentence of behavior, name, type, count, place, order, or condition; give
the code line for each one, or "missing"; compare them; each requirement
that is not met is a blocker. The run used only the new requirements lens
(`--lenses requirements`). Its answers then replaced the requirements
answers of the earlier run, with the other lenses as they were.

| Requirements lens | TA | FA | TR | FR | False approvals |
| --- | --- | --- | --- | --- | --- |
| Earlier prompt | 10 | 2 | 14 | 12 | 24, 28 |
| In steps | 11 | 4 | 12 | 11 | 24, 27, 28, 37 |

The lens in steps approved case 27 in each sample and added two false
approvals, so the prompt did not change. A rule in the harness finds case
27 (the section above). Rules in prompts did not help the reviewer in two
tries: a mechanical defect goes in a harness rule, and the spec step of the
apprentice (`primitives/apprentice/README.md`) puts the sentences of the
task in front of the worker before the work.
