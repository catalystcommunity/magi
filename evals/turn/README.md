# Turn eval and comparison verdict

The turn eval runs whole persona turns and checks their effects. The
comparison verdict runs it two times, on a baseline and on a candidate,
and says if the candidate is permitted (`docs/decisions/0006`).

Use it for each change to a persona prompt, a tool description, the
persona turn, the tool gate, or the context that a turn gets.

## How it runs

`magi/compose/turneval_test.go` starts the real conversation, knowledge,
and workflow services on a test database, in the test process. There is
no installation and no network, only the model server. Each run of a case
has a new person, so that runs do not see each other.

A case has:

- `persona`: the persona that gets the messages.
- `core`: true for behavior that must not get worse.
- `given`: tool calls that run before the first message (facts, to-dos).
- `steps`: one message of the person each, with the checks of its turn.
  A step with `event` in place of `say` starts the turn from an event:
  the harness makes a task that this persona started (`task`: a `title`,
  and `state` `review`, `stopped`, or `blocked`), does its steps as a runner does,
  and sends the report that `event` names.

The checks read the turn log and the reply:

| Check | Passes when |
| --- | --- |
| `tools_all` | each tool has a call that did its work |
| `tools_any` | one of the tools has a call that did its work |
| `tools_called` | each tool has a call that code did not refuse |
| `tools_none` | no call of these tools, also no refused one |
| `refused` | each tool has a call that code refused |
| `reply_all`, `reply_any`, `reply_none` | regular expressions on the reply, with no difference of case |
| `max_calls` | the turn has at most this number of tool calls |

No model judges a result. A check that needs a judge is not in this set.

## Sets

| Set | Cases | Contents |
| --- | --- | --- |
| `core-v1.json` | 19 | Turns from a message of the person |
| `core-v2.json` | 23 | `core-v1`, and 4 turns from an event (`v01` to `v04`, not core until they pass each run of two baselines) |

The default set of the verdict is the newest.

## Rules

- Do not change a set after a verdict used it. Make a new version
  (`core-v2.json`).
- Use only synthetic data.
- Use a local model server that serves no other traffic. An inference
  host that stops its engine when the machine compiles is not usable:
  the eval compiles Go code.

## The verdict

```sh
python3 -I evals/turn/verdict.py --base HEAD \
  --url http://127.0.0.1:18092/v1 --model gemma-4-12b --runs 3
```

The baseline is the commit of `--base`, in a temporary work tree. The
candidate is the working tree. The baseline must have the eval harness.
The results of a baseline are kept in `results/` (not in git) and used
again for the same commit, set, model, and number of runs.

The command needs `initdb` on `PATH` (the runner tools), or
`MAGI_TEST_DATABASE_URL`.

The verdict is FAIL when:

- a core case that the baseline passes in each run fails in half of the
  runs of the candidate, or more; or
- the candidate passes fewer runs in total than the baseline, by more
  than the noise. The noise is the largest difference of the totals
  between two runs of the baseline (at least 1), times the square root
  of the number of runs.

The verdict is ERROR, not a result, when the model service failed in a
run, or when the two sides did not run the same cases.

Exit status: 0 pass, 1 fail, 2 error.

## Results

| Date | Set | Model | Baseline | Result |
| --- | --- | --- | --- | --- |
| 2026-11-19 | core-v1 | gemma-4-12b (Q4_K_M, RTX 4080) | first run of the set | 19 of 19 in 1 run, 101 s |
| 2026-11-19 | core-v1 | gemma-4-12b | 19c6c64, 3 runs: 57 of 57 | Candidate with the task tools of the assistant removed (a test of the verdict, 2 runs): FAIL, 5 core cases |
| 2026-11-19 | core-v1 | gemma-4-12b | 19c6c64, 3 runs: 57 of 57 | Candidate: the rule "You cannot approve, merge, or release a task" removed from the prompt of the default persona (code refuses the tool). 56 of 57: PASS, no change |
| 2026-11-20 | core-v1 | gemma-4-12b | 07ce0f6, 3 runs: 57 of 57 | Candidate: event turns and the dispatch. 57 of 57: PASS, no change |
| 2026-11-20 | core-v1 | gemma-4-12b | 6e6a6ad, 3 runs: 57 of 57 | Candidate: `decide_task` with resume and check. 57 of 57: PASS, no change |
| 2026-11-20 | core-v2 | gemma-4-12b | no baseline | Cases `v01` to `v04` only, 3 runs: 12 of 12 |
| 2026-11-20 | core-v2 | gemma-4-26b-a4b (UD-Q4_K_M, RTX 4080, the experts of 16 layers on the processor, 33 tokens/s) | no baseline | 2 runs: 42 of 46 in 553 s. `p02` and `e02` (core) fail in each run. About 2 times slower for each case than gemma-4-12b on this card, and not better |
