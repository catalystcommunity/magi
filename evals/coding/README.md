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

## Results: Qwen3.6-35B-A3B on an RTX 3090 with MTP, model review on (2026-11-18)

The candidate is `qwen3.6-35b-a3b-vr1` (MTP n3, about 200 tokens/s), with
temperature 0.6. The runner ran the lens review of `prereview.py` after the
checks, as a hint in the review request.

| Case | Check | Steps | Minutes | Minutes on an RTX 4080 | Grade | My defects | Found by the model review | False blockers |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| pr2 | pass (1) | 86 | 23 | 39 | revise | 7 | 3 | 0 |
| pr3 | pass (1) | 29 | 20 | 59 | revise | 6 | 2 | 1 |
| pr4 | pass (1) | 64 | 25 | 60 | revise | 5 | 1 | 0 |
| pr5 | pass (1) | 49 | 24 | 35 | revise | 5 | 1 | 0 |
| pr6 | pass (1) | 115 | 31 | stopped at 150 | revise | 7 | 1 | 0 |
| pr7 | pass (1) | 36 | 20 | stopped at 150 | revise (small) | 4 | 0 | 0 |

All 6 cases finish, 2 to 5 times faster than on the 4080. The person still
sends each change back. The model review finds 8 of 34 defects, and 1 of
its 9 distinct blockers is false. It finds differences from the task's
words, and tests that copy the code. It does not find:

- a block of code that is copied in one file (pr3, pr4, pr5). The diff
  gate of the apprentice now finds it (`diffgate.py`);
- a call that the contract of an operation does not allow (pr6: a lookup
  by an id that put-entity never makes). The review context now has the
  contract text of each request type that the change uses;
- a test that passes without the change (pr7), and a test that skips
  without a database (pr6);
- logic in the calling loop: a rejection that uses up a check retry (pr4),
  an import that runs every 5 s for the life of the server (pr6).

The defects that the review finds are real, so the next run gives them to
the apprentice before it finishes (`apprentice.work_review`).

## Results: the same model with the review in the work round (2026-11-18)

Runner 5060b66: at the first finish that passes the check, the apprentice
gets the copy rule of the diff gate, the test gate (`testgate.py`), and the
lens review (2 samples, with the contract context), then the checklist.

| Case | Check | Steps | Minutes | Grade | Defects (before) | What the round did |
| --- | --- | --- | --- | --- | --- | --- |
| pr2 | pass (1) | 89 | 28 | revise | 4 (7) | review: 6 real blockers, all fixed (one was a replaced SYSTEM_PROMPT) |
| pr3 | pass (1) | 65 | 29 | revise (small) | 3 (6) | copy rule: one helper; test gate: 2 tests of "nothing happens", kept |
| pr4 | pass (1) | 117 | 32 | revise | 3 (5) | review: new files missing from the diff, fixed |
| pr5 | pass (1) | 85 | 34 | revise (small) | 3 (5) | copy rule: one helper; test gate: Go tests that copied the logic, fixed; 1 false blocker, rebutted |
| pr6 | - (loop) | 141 | 30 | fail: no change | - (7) | review: 10 blockers, most real; the apprentice broke its edits and the run stopped in a loop |
| pr7 | pass (1) | 81 | 31 | approve, small comment | 2 minor (4) | test gate: a test that could not fail, fixed; review: a missing test, added; 1 false blocker, rebutted |

The defects go from 34 to 15 on the cases with a change, and the first
change that a person can approve comes out (pr7). Each case takes 6 to 11
minutes more. The apprentice rebutted each false blocker with a reason and
changed no correct code for it.

pr6 lost the change that passed the check at step 97. The harness now
keeps that change before a review round and restores it when the run ends
without a passing check (895ab40).

Defects that no check finds yet, in all runs of a case: the task's words of
place and count ("one line", "after N steps"), new code outside the crash
guard, no test through the entry point (run_task, Runner.work), and a
return value that the calling loop counts as a failed check (pr4).

## Results: pass 3, with the snapshot before a review round (2026-11-19)

Runner 895ab40: the same harness as pass 2, and the change that passed the
check is kept before a review round.

| Case | Check | Steps | Minutes | Grade | Defects (pass 2) | What the round did |
| --- | --- | --- | --- | --- | --- | --- |
| pr2 | pass (1) | 74 | 44 | revise | 5 (4) | review: 8 real blockers, all fixed |
| pr3 | pass (1) | 46 | 54 | revise | 4 (3) | review: 1 false blocker |
| pr4 | pass (1) | 61 | 44 | revise | 5 (3) | review: 2 blockers from review lessons; the fix of one made a defect |
| pr5 | pass (1) | 63 | 60 | revise | 3 (3) | review: a copied-logic test (real, not fixed) and 1 false blocker |
| pr6 | pass (2) | 150 | over 60 | revise | 6 (no change) | a second work round fixed 2 failed tests |
| pr7 | pass (2) | 47 | 56 | revise (small) | 2 (2) | review: approve |

pr6 now ends with a change. On the other 5 cases, the defects go from 15 to
19. One sample of each case has a large spread, so this is no real change.

Findings:

- The defects that stay are sentences of the task that the change does
  differently: "through the same Tools object", "one exec", "if that call
  fails, keep the current summary text", "when there is no cost, leave the
  outputs out", "does not count as a failed check".
- A review lesson can make a defect. In pr4 the work review asked for
  `git add --intent-to-add` before the diff (rl-0004). The fix added a
  second exec, against "one exec" of the task, and left entries in the
  index.
- The eval runs each case at its base commit, with the `tools/check.sh` of
  that commit. Those versions do not run the database tests. In pr6 the
  three import tests skip in the check, and they fail on a database (a
  database name with capitals and no quotes). The check of the current
  main runs the database tests, so a run on main finds this.

## Results: pass 4, the spec step adds acceptance checks (2026-11-19)

The harness of pass 3, and `--checks-dir checks/spec1`: the checks that
the spec step wrote from the instructions only (Qwen3.6-35B-A3B, about
22 s for each case). Label `qwen3.6-35b-a3b-vr1-spec1`.

| Case | Check | Steps | Minutes | Grade | Defects (pass 3) | Task sentences missed (pass 3) | dbcheck |
| --- | --- | --- | --- | --- | --- | --- | --- |
| pr2 | pass (1) | 67 | 44 | revise | 3 (5) | 1 (2) | no Go module |
| pr3 | pass (1) | 54 | 40 | revise (small) | 3 (4) | 0 (1) | no Go module |
| pr4 | fail (1), see below | 79 | 46 | revise | 2 (5) | 0 (1) | no diff |
| pr5 | pass (1) | 70 | 41 | revise | 3 (3) | 0 (1) | pass |
| pr6 | pass (2) | 184 | 47 | revise | 4 (6) | 2 (3) | pass (pass 3: fail) |
| pr7 | pass (1) | 33 | 24 | approve (small) | 1 (2) | 0 (1) | no Go module |

The defects go from 25 to 16. The missed task sentences go from 9 to 3:
"through the same Tools object", "if that call fails, keep the current
summary text", "when there is no cost, leave the outputs out", and "does
not count as a failed check" are now correct. One sample of each case has
a large spread, but the drop is in the class of defect that the checks
name.

Findings:

- A check with a low seam makes the agent test at that seam, and can make
  it change the code. For pr5 the check named "the work step parsing and
  output generation function". The agent made that function, and the
  refactor made a regression: when the apprentice writes no JSON line, the
  work step writes into a nil map. For pr2 the check named build_prompt,
  and the tests do not go through the run. The prompt now names only the
  entry point of the flow, and no invented function. The apprentice runs
  the spec step itself now (see `primitives/apprentice/README.md`).
- pr4: the apprentice check passed two times. The check step of the
  workflow failed in `primitives/inference` (the test race that 2ba1809
  fixes, at a base before the fix), so the run kept no diff. The grade
  uses the last `git diff` of the transcript. Its one defect came again
  from rl-0004: the work review asked for `git add --intent-to-add`, and
  the fix left entries in the index. rl-0004 now names the reset.
- The retry of pr6 ("the knowledge service can start later") is the
  missed sentence that stays: the import ignores the errors of the
  service, so the retry loop never runs again.

## Results: three candidates on an RTX 3090 (2026-11-21)

The harness of runner 0a931e9, with `--checks-dir checks/spec1`, on the
cases pr3, pr5 and pr7. The review model of the runner is the candidate.
Each model has 64k context and a KV cache of 8 bits. The baseline is
pass 4 of Qwen3.6-35B-A3B (about 200 tokens/s with MTP).

| Model | Tokens/s | Case | Check | Steps | Minutes | Grade | Defects | Baseline |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Qwen3.6-27B Q4_K_M | 41 | pr3 | pass (1) | 46 | 78 | revise | 3 | revise (small), 3, 40 min |
| | | pr5 | pass (1) | 78 | 65 | revise (small) | 2 | revise, 3, 41 min |
| | | pr7 | pass (1) | 78 | 69 | revise (small) | 2 | approve (small), 1, 24 min |
| Devstral Small 2 24B Q4_K_M | 47 | pr3 | no change | 72 | 48 | blocked: its new tests fail | - | |
| | | pr5 | pass (2) | 230 | 64 | revise | 4 | |
| | | pr7 | pass (1) | 96 | 37 | reject | 7 | |
| Gemma 4 26B A4B UD-Q4_K_M | 107 | pr3 | fail | 108 | 94 | time limit, then the check did not return in 30 minutes | - | |
| | | pr5 | no change | 81 | 21 | blocked: failed edits | - | |
| | | pr7 | no change | 35 | 24 | blocked: failed edits | - | |

Findings:

- Qwen3.6-27B makes changes of the same quality as Qwen3.6-35B-A3B: 7
  defects in 3 cases for each model. On pr5 it makes the same refactor of
  the work step and keeps the behavior, where the baseline made a
  regression. On pr7 it misses "does not count as a failed check". It
  needs 212 minutes for the 3 cases, against 105: the model is 5 times
  slower, and the review and the check add fixed time.
- Devstral Small 2 writes tests that do not test the change: a Go test
  that parses JSON into its own struct (pr5), a test that mocks the
  rejection that it tests (pr7). It deletes code that the task did not
  name (pr7). Its chat template refuses two messages of the same role in
  sequence, which the apprentice and the persona turns send. These runs
  use a template without that rule.
- Gemma 4 26B A4B cannot make edits in this harness. `edit_file` fails
  again and again, because `old_string` is not the text of the file. One
  reply had the tool call as text, which the server did not parse. With
  no limit on its thinking, its replies were cut 6 times in one run; a
  reasoning budget of 4096 tokens removes that.
- A slow model showed a defect of the stall check: the check, the review
  and a long tool call wrote no status line, so the stall check stopped
  three good runs of Qwen3.6-27B. Each of these phases writes a status
  line now, and the stall limit is 30 minutes.

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

`--checks-dir DIR` adds the acceptance checks of `DIR/<id>.md` at the end
of the instructions, under "Acceptance checks:". A spec step makes these
checks before the eval (`spec_step.py`, with the prompt of
`primitives/apprentice/magi_apprentice/spec.py`, output in
`checks/<name>/`): a model reads only the instructions, and writes
one check for each sentence that asks for a behavior (the quote, the
result that a test can compare, and the highest seam). Use a new
`--label` for such a run.
A runner with the spec step of the apprentice makes its own checks for
each run (with a review model). With `--checks-dir`, the run keeps the
fixed checks of the directory, so that two runs get the same checks.

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

Then run the database tests of the changed Go modules:

```sh
PATH=$TOOLS/postgres/bin:$PATH python3 -I evals/coding/dbcheck.py evals/coding/results/<label>
```

The `tools/check.sh` of an old base can skip the database tests.
`dbcheck.py` applies `diff.patch` of each case to a worktree at the base.
It finds the Go modules of the changed files and runs their tests with
`tools/test-db.sh` of the current repository. It writes `db.json` (the
outcome and the failed modules) and `db.output.txt` next to the diff. On
pass 3 it finds the failed import tests of pr6. A failed module is a
defect of the change.

Then run the probes:

```sh
python3 -I evals/coding/probe.py evals/coding/results/<label>
```

A probe is one test of one sentence of the task. It uses only code that
exists at the base of the case, so it does not depend on the names that
the candidate chose. `probes/<id>.py` and `probes/<id>_test.go` hold the
probes of a case; the first line of each file names the directory where
the file runs. `probe.py` applies `diff.patch` to a worktree at the base,
copies the probe files in, and runs each probe. It writes `probe.json`
next to the diff:

- `passed` and `failed`: the probes, each failed probe with its sentence;
- `removed_tests`: the tests of the base that the change removed;
- `other_files`: the files that the change touches and the approved
  change does not.

A failed probe is a defect of the change, and no person has to find it.
A probe set does not find each defect: style, copied code, and a test
that does not test the change stay with the review. `--approved` runs the
probes on the approved change of each case. Each probe must pass there;
run it when you add a probe. When a grade finds a defect of behavior that
no probe finds, add a probe for it.

Each case has probes. On the results of 2026-11-21:

| Change | Probes | Failed probes |
| --- | --- | --- |
| Qwen3.6-35B-A3B pr2 | 8 of 10 | the entries are not one line; a long file is cut at `tool_output_chars`, not at 20000 |
| Qwen3.6-35B-A3B pr3 | 11 of 13 | an error text at the time limit; the last call is not counted in the usage |
| Qwen3.6-35B-A3B pr5 | 11 of 13 | output that is not JSON; the status error |
| Qwen3.6-35B-A3B pr6 | 6 of 7 | an import that the service refuses returns no error, so no retry runs |
| Qwen3.6-35B-A3B pr7 | 10 of 10 | |
| Qwen3.6-27B pr3 | 11 of 13 | the stop text; the last call is not counted or recorded |
| Qwen3.6-27B pr5 | 13 of 13 | |
| Qwen3.6-27B pr7 | 9 of 10 | the stub uses up a check retry |
| Devstral Small 2 pr5 | 12 of 13 | null for `prompt_tokens_details` |
| Devstral Small 2 pr7 | 8 of 10 | the reset is before the diff, so the diff shows no new file |

Each failed probe is a defect that the grade of a person found, except
two. The grade of pr3 for Qwen3.6-35B-A3B did not see that the usage
leaves out the last call. The grade of pr2 did not see that the change
reads the instructions file with the `read_file` tool of the model, which
cuts the text at `tool_output_chars`.

Grade each run, then close it:

```sh
magi decide RUN reject
```

### The verdict for a harness change

One run of a case says little. The same model with the same settings got
8 of 10 probes of pr2 in one run and 2 of 10 in the next run. Thus a
change of the harness gets a verdict from two or more runs of each version
(decision record 0006).

1. Run the cases two or more times with the version before the change.
   Give each run its own label. Run `probe.py` on each result directory.
2. Release the change to the runner. Run the same cases with new labels.
   Run `probe.py` on each result directory.
3. Get the verdict:

```sh
python3 -I evals/coding/verdict.py --baseline base-1,base-2 --candidate cand-1,cand-2
```

The score of a run is the sum of the parts of the probes that pass, one
part for each case. The verdict is `pass` when the mean of the candidate
is above the mean of the baseline by more than the spread of the baseline
runs. It is `fail` when the mean is below by more than the spread, or
when a probe passes in each baseline run and fails in each candidate run.
A smaller difference is `no change`. One baseline run gives `no verdict`.
The exit status is 0, 1, 2, or 3 in that sequence.

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
