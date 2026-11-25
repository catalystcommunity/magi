# apprentice

The apprentice is the Magi coding harness: a model loop with tools in an
execution environment. Magi owns this code. It does not wrap another agent
product.

"Apprentice" is the name of this component only. The program on a machine
that connects to Magi and runs steps is a runner. The identity that a
person talks to is a persona.

## Purpose

The apprentice does one coding task in a Magi execution environment:

1. It gets a task from the workflow queue (worker mode) or from the command
   line (one-shot mode).
2. It sends the task to a model through the OpenAI-compatible Chat Completions
   API with tool calling.
3. It runs the tools that the model selects. All effects go through the
   ExecutionService (`exec`, `read-file`, `write-file`).
4. It records each model request, model response, tool call, and tool result
   as JSONL. At the end, it uploads this transcript to the ArtifactService.
5. It completes the workflow step with a summary.

The apprentice does not commit or push. Separate workflow steps do git operations,
unless the task instructions tell the apprentice to do them.

## Context at the start

Before the first model call, the harness reads the work directory and adds
sections to the first user message:

- **Project instructions.** The text of `AGENTS.md` in the work directory. When
  there is no `AGENTS.md`, the harness reads `CLAUDE.md`. The text is cut at
  20000 characters; a note says how much is not shown. When neither file is
  there, the section is left out. A missing file is not an error.
- **Repository.** The output of `git status --short` (at most 50 lines), of
  `git log --oneline -5`, and the top-level entries of the work directory on one
  line. One `exec` call gives all three. A part whose command failed is left out.
  A clean work tree gives the line `(no changes)`.
- **Lessons.** The review lessons of this repository that relate to the task,
  from the `lessons` var. The section is left out when the var is empty.

The harness reads the files and runs the command through the ExecutionService,
in the same way as the tools. It does not read the files of the host that the
harness runs on.

After the work directory line, the first user message states the budget. See
the section "Limits and budget".

The system prompt says that the project instructions are the rules of the
repository owner, and that the agent must follow them, unless the task
instructions say otherwise.

## Spec step

When a review model is set (`--review-model`), the harness makes one call to
that model before the work starts. The model reads only the task
instructions. It writes one acceptance check for each sentence that asks for
a behavior: the quote, a result that a test can compare, and the entry point
where a test sees the result. The harness adds the checks at the end of the
instructions, under "Acceptance checks:". The worker, the diff rules, the
self-review, and the work review then read them as a part of the task.

The prompt is in `magi_apprentice/spec.py`. The call uses temperature 0.3
and at most 12000 output tokens. The transcript gets a `spec_step` record
with the model, the length of the checks, and the time. A failed call gives
no checks, and the task continues without them. A task that already has a
line "Acceptance checks:" keeps its own checks. Set
`MAGI_APPRENTICE_SPEC_STEP` to `false` to turn the step off.

Why: in the coding eval (`evals/coding`, pass 4), the checks cut the task
sentences that the change did differently from 9 to 3 on six cases.

## Tools

| Tool | Function |
| --- | --- |
| `bash` | Runs `bash -lc <command>` in the work directory. Has a timeout. Returns the exit code, stdout, and stderr. |
| `read_file` | Reads a file with line numbers. `offset` and `limit` select lines. |
| `write_file` | Creates or replaces a file. Creates parent directories. |
| `edit_file` | Replaces an exact string. The string must be unique, unless `replace_all` is true. |
| `list_files` | Lists files below a directory (skips `.git`). An optional glob filters them. |
| `search` | Searches file contents with ripgrep (or `grep -rE` if ripgrep is not installed). |
| `finish` | Stops the task with a summary. |

When a check command is set, the harness runs it when the model calls
`finish`. If the check fails, the harness rejects `finish` and shows the
output to the model. After `MAGI_APPRENTICE_CHECK_RETRIES` failures, the task
finishes with `apprentice.check_passed` = `false`.

## Context management

- Each tool result in the context is cut to `MAGI_APPRENTICE_TOOL_OUTPUT_CHARS`.
  The head and the tail stay; the middle is removed.
- The harness estimates the prompt size. It calibrates the estimate with the
  token counts that the server returns. When the estimate is more than
  `MAGI_APPRENTICE_CONTEXT_TOKENS`, the harness replaces old tool results and old
  large tool arguments with short notes. The arguments of `finish` calls stay
  whole. A `finish` summary that is a compaction stub is rejected. If this is
  not sufficient, it removes the oldest turns. The system prompt and the task
  always stay.
- Before each model call, the harness calls a steering hook. The hook returns
  operator notes. The default hook returns no notes, because the workflow
  service cannot send messages to a worker yet.

## Limits and budget

The first user message states the budget. It gives the number of model calls
and the time limit.

The harness sends a budget note as a user message before a model call. It sends
a note at these points:

- 80% of the model calls are used.
- 5 model calls are left.
- 80% of the time limit is used.

Each note goes in once. The note says how many model calls and how many seconds
are left. It tells the model to finish the necessary work, run the check, and
call `finish` with a summary. It also tells the model not to start new
improvements. The transcript records each note as `budget_note`.

When a limit stops the loop and the model did not call `finish`, the harness
makes one more model call. This call has no tools. The message asks for the
finish summary: what changed, how it was verified, and what is left undone. The
reply becomes `apprentice.summary`. The status stays `step_limit` or
`time_limit`. When that call fails, the harness keeps the summary text that
says that the task stopped at the limit.

## Question mode

In question mode the task is a question about the repository, not a
change. The summary of `finish` is the answer. Magi uses this mode for an
investigation (`magi/README.md`, "Investigation workflow").

- The system prompt tells the model to answer from the repository: search,
  read, and run commands, and give the conclusion first, then the evidence
  with `path:line` or the command and its output, then what it did not
  find.
- The run has no check command, no diff rules, no test of new tests, no
  self-review, and no spec step. The guard for steps with no change to a
  file is off. The other guards apply.
- The first `finish` whose answer names no file with a line is rejected one
  time, with a request for the evidence. The second answer is accepted, so
  "I did not find it" is a possible answer.
- At a limit, the last model call asks for the answer from what the model
  found, and for what is still open.
- The model can write a temporary file or a small test. The environment is
  removed after the answer, thus no change is kept.

Start it with `--question` in one-shot mode, or with the run variable
`mode` with the value `question` in worker mode. A worker gives a question
at most 60 model calls and 900 seconds.

## Thinking cuts

A thinking model can spend the whole output limit on reasoning. The reply then
has `finish_reason` `length`, no content, and no tool call. A server with a
reasoning budget ends the thinking itself: the reply then stops normally, but it
has reasoning, no content, and no tool call. The harness handles the two cases
the same way. It answers
with a note that tells the model not to think about the whole task again, and to
call one tool now. A cut reply that has content keeps the note about writing
large files in parts. The transcript records the reply as `thinking_cut`, with
the step and the size of the reasoning in characters. The `model_response`
record holds the size, and the first and the last 400 characters of the
reasoning when it is longer than 800 characters. The reasoning does not go back
to the model. After a thinking cut, the harness compacts the context when the
estimate is above 60% of `MAGI_APPRENTICE_CONTEXT_TOKENS`, because a long
context makes long thinking more likely. Three replies in a row without a tool
call still end the task as `stalled`. The summary then says that the replies
were cut while the model was thinking, and how many times. Six thinking cuts
in one run also end the task as `stalled`, also when tool calls come between
them.

## Run guards

Rules in code (`guards.py`) stop a run that goes wrong. They do not call a
model. Each rule first sends a note to the model, and then ends the run when
the model does not change its approach. The transcript records each rule as
`guard`.

| Rule | Note | End |
| --- | --- | --- |
| The same tool call with the same result, in the last 10 calls | at 3 | at 5, status `loop` |
| The same error lines from different commands (numbers and `/tmp` paths removed) | at 3 | at 5, status `loop` |
| No `edit_file` or `write_file` | after 40 steps (25 in a round with a review comment) | after 120 steps (75), status `no_progress` |
| An environment error in a failed tool result (for example `Connection reset by peer`, `no space left on device`) | once for each kind: do not fix the environment, report it | none |

The end of a run by a rule makes one more model call without tools, for the
summary, as at a limit.

## Diff rules

When the model calls `finish`, the harness checks the diff of the work
directory with fixed rules (`diffgate.py`) before the check command. Each
rule comes from a correction that a reviewer made more than once.

| Rule | Instruction to the model |
| --- | --- |
| A file lost its executable bit (`git diff --summary` shows `100755 => 100644`) | run `chmod 755` on the file |
| The change deletes a test file, and the task does not say to remove tests | restore the file, or tell why it must go |
| The change adds the same block of 4 code lines in two places of one file (tests, generated files, and error boilerplate are not counted) | put the code in one helper or constant, or tell why the copies must stay |
| A Python test file of the change adds a class with `Fake`, `Mock`, or `Stub` in its name | add a test that goes through the real object, or tell why no real object can be used |

The copy rule is a hint. The harness gives it one time in a run. On the
last 300 commits of main, it found 5 commits of approved code.

The fake rule is a hint too. A test on a fake that the change wrote agrees
with the change, not with the real object. In evals/coding pr2, a change
passed its tests, the check, and the review, and stopped with a
`TypeError` on the real `Tools` object: its tests used a new fake. In 90
days of main, only two large first commits added such a class.

A rejection by these rules does not use up a check retry. After 2
rejections in one run, the harness accepts the finish, so that a rule
cannot hold a run. The transcript records each rejection as `diff_gate`.

## Test of the new tests

After the first finish that passes the check, the harness runs the new
tests of the change on the code without the change (`testgate.py`). It
makes a git worktree of HEAD, copies the changed test files into it, runs
only the new test functions (Go `TestX`, Python `test_x`), and removes the
worktree. The work directory does not change. A new test that passes there
does not test the change. The harness sends the names to the model one
time in a run. The model fixes each test, or says in the summary why the
test can stay. A test that fails or does not compile without the change is
good.

The transcript records the result as `test_gate`. Set
`MAGI_APPRENTICE_TEST_GATE` to `false` to turn it off.

On the coding eval, the gate finds the tests that copy the logic of the
code (pr5, two runs) and a test whose input never reaches the change (pr7).
On the 38 cases of evals/review, it also finds a test in 2 of the 22
changes that a person approved: tests that check that something does not
happen.

## Model errors

A model error has a kind. A 402, or a 429 whose error type says that the money
or the quota is used up (for example `BudgetLimitError`), ends the call at
once with no retry: status `blocked_provider`. A call that fails after all its
retries is `model_unavailable`. Other errors are `error`.

## Self-review

When the model calls `finish` for the first time, and the check passed (or
there is no check command), the harness does not end the task. The harness runs
one `exec` call in the work directory. The call puts new files in the git index
with `git add --intent-to-add --all`. Then it runs `git diff --stat` and
`git diff`. This way the diff also shows new files. At the end, the call runs
`git reset -q`, so the index is as it was before the review. The diff is cut to
`MAGI_APPRENTICE_TOOL_OUTPUT_CHARS` characters.

The harness rejects the `finish` call. The tool result has the diff and a
review text. The text tells the model to review the change as a strict reviewer
of the repository: each part of the task instructions, the project
instructions, no unrelated change, no debug output, tests for each new
behavior, and clear names and comments. Then the model calls `finish` again
with the summary. When it changes files, it runs the check again first.

The second `finish` call ends the task as usual. The check gate applies to it.
The review happens once in each run. The transcript records the review as
`self_review`, with the size of the diff in characters.

The review does not happen in these cases:

- `MAGI_APPRENTICE_SELF_REVIEW` is `false`.
- Fewer than 5 model calls are left.
- 80% of the time limit is used.

In the last two cases, the first `finish` call ends the task. The transcript
records the review as skipped near a limit.

## Reviewers

Two reviewers give a verdict on a finished change: `approve` or `revise`.
`evals/review` measures them against the decisions of a person.

- `review.py` reads the task, the review lessons, and the diff. Generated
  files are named, but their diff is not shown. The model gives its verdict
  in a JSON block.
- `review_agent.py` gets the same text, and read-only tools on a checkout
  of the change: `read_file`, `list_files`, `search`, and `git` with `show`,
  `log`, `diff`, `blame`, `grep`, and `ls-files`. It cannot edit, build, or
  run tests. It gives its verdict with the tool `verdict`.

The same rules apply to both. A blocker or an unmet requirement makes the
verdict `revise`. An answer with no valid verdict is `invalid`, and it
cannot approve. With more than one sample, the change is approved only
when every sample approves.

### Model review before the review of a person

`python -m magi_apprentice review --base COMMIT --instructions @FILE
--environment ENV --model NAME` reviews the change from COMMIT to the work
directory (`prereview.py`). The runner runs it in the check step, after
the checks pass, when `apprentice.review_model` is set in its
configuration. The summary goes into the review request, and the verdict
goes into the output `review.model_verdict`. A person still decides.

- The harness collects the context (`review_context.py`).
- Each lens of `review.py` gets one call: requirements, text, logic,
  tests, scope. The prompt does not say that the checks passed: with that
  text, the reviewer trusted the change and approved 7 of 16 changes that
  a person sent back (evals/review).
- The harness makes a blocker minor when the checks disprove it: a compile
  error or an undefined name, or a file mode that the patch does not change.
- A failed review is a summary that says so. The check step goes on.

The same review can run in the work round. `python -m magi_apprentice run
--review-model NAME` reviews the first finish that passes the check gate,
instead of the checklist of `SELF_REVIEW`. The apprentice gets the
blockers of two review samples, and then the checklist. It fixes a real
blocker, and it says in the finish summary why a false one is false. An
approval, a failed review, or a revise with no blockers gets the checklist
only: an approval of one review is not reliable.

With `--tester` (or `MAGI_APPRENTICE_TESTER=true`) and a review model, the
tester runs before the work review (`tester.py`). The tester is a second
run of the worker model with an empty context. It gets the sentences of
the task with a number each (S1, S2, ...), and the work directory with the
change. It writes one test file (`test_tester_probe.py` or
`tester_probe_test.go`) and runs it. It has 40 model calls and 20 minutes.
The harness does these parts itself:

- The finish of the tester goes back when a sentence has no test and no
  line that says why it has none, or when a Python test file of the
  tester has a fake, a stub, or a mock of its own.
- The harness runs the test file. The report of the tester is not used.
- A finding is a test that stops with an exception in a file of the
  change (a Go panic, or a Python traceback through such a file that does
  not end in a test file and is not an `AssertionError`). The tests that
  fail in a different way stay in the transcript only.
- The harness puts the work directory back as it was before the tester,
  so the tester cannot change the work.

Each finding goes to the apprentice before the blockers of the review,
with the sentence of the task, the failure, and the source of the test.
The transcript has the records of the tester with the prefix `tester_`,
and one record `tester` with the failed tests and the findings. The tester
is off by default. The runner passes the flag when `apprentice.tester` is
true and the work review is on.

Why only an exception is a finding: on fixed changes with known defects
(2026-11-21, the worker model on an RTX 3090), the tests of the tester
that failed on an assertion were wrong each time, also on two changes
that a person approved. In three of three runs, the tester found a change
that passed its check and its review and stopped with a `TypeError` on the
real `Tools` object (the tests of that change used a fake). It gave no
finding on nine other changes, four of them approved by a person. It did
not find the defects where the code runs and gives a wrong value.

Why it is off by default: in a comparison of two runs with the tester and
two runs without it, on three cases (2026-11-22), the tester gave no
finding in six runs and the verdict was no change. Each run of the tester
added 5 to 20 minutes.

Before a review round (the test gate, the work review, or the checklist),
the harness keeps the change that passed the check as a git tree object
(`git write-tree`). When the run then ends without a passing check (a loop,
a limit, a failed check), the harness restores that change and finishes
with its summary and a note. Thus a review round cannot lose a change that
passed the check. In evals/coding pr6, 10 review blockers made the
apprentice break its edits, and the run stopped in a loop with no change. The runner passes the flag
when `apprentice.work_review` is true and `apprentice.review_model` is set.

## Worker mode

```sh
python -m magi_apprentice worker [--queue apprentice] [--max-tasks N]
```

The worker claims a step from the queue, sends heartbeats on a background
thread, and sends progress notes: at the start, after each
`MAGI_APPRENTICE_PROGRESS_EVERY` tool calls, and at the end. The check
command and the review of the change write a note that starts with
"working:" when they start. A model call or a tool call also writes one
when there was no note for `MAGI_APPRENTICE_ALIVE_EVERY` seconds. The
stall check of Magi stops a task that writes no note for too long, so the
longest time with no note is this time plus one operation.

Assignment vars:

| Var | Use |
| --- | --- |
| `environment` | Execution environment ID. Required. |
| `instructions` | Task text. Required. |
| `workdir` | Work directory. Default `/work/repo`. |
| `title` | Short task title. Optional. |
| `check_command` | Check command. Default `MAGI_APPRENTICE_CHECK_COMMAND`. |
| `check_output` | Output of a failed check from an earlier attempt. |
| `review.comment` | Changes that a reviewer asked for. |
| `lessons` | Review lessons of this repository that relate to the task. The first message shows them before the task instructions. |
| `model` | A model name, or JSON with `name`, `temperature`, `max_tokens`, `max_steps`. The base URL and the key cannot change through vars. |

Outputs: `apprentice.summary`, `apprentice.transcript` (artifact digest), `apprentice.steps`,
`apprentice.status`, `apprentice.tool_calls`, `apprentice.check_passed` (when a check
ran), and `apprentice.blocked` (`true` after a blocked round, empty after the others). After
a blocked round, the next round gets `blocked.comment` (the comment of the person's
decision) in place of `review.comment`.

Outcomes:

- `done`: the apprentice finished, or it stopped at a step or time limit
  (`apprentice.status` tells which). The workflow checks are the independent
  judgment.
- `blocked`: status `loop`, `no_progress`, `stalled`, `blocked_provider`, or
  `model_unavailable`. The work stays in the environment, and a person
  decides (step `blocked` of the code-change workflow). `apprentice.error` has
  the error of the model call.
- `fail`: an error that the apprentice cannot recover from, for example a model
  that does not answer. `error` has the cause.
- `retry`: the worker got SIGTERM during the task.
- No completion: the claim was lost (for example, the run was cancelled).

## One-shot mode

```sh
# In an execution environment:
python -m magi_apprentice run --environment env_123 --instructions "Fix the failing test"
# On this host, in a directory (no isolation; for tests only):
python -m magi_apprentice run --local /tmp/work --instructions @task.txt \
  --check-command "python3 -m unittest" --transcript /tmp/t.jsonl
```

With `--question`, the instructions are a question, and the summary of the
result is the answer (see "Question mode").

The command prints a JSON result. The exit code is 0 when the task finished
and no check failed. The result has `usage` with the totals of the task:
`prompt_tokens`, `completion_tokens`, `cached_tokens`, and `cost` in US
dollars.

## Configuration

| Variable | Default | Use |
| --- | --- | --- |
| `MAGI_MODEL_BASE_URL` | none (required) | Chat Completions base URL |
| `MAGI_MODEL_NAME` | `magi-coder` | Model name |
| `MAGI_MODEL_API_KEY_FILE` | none | File with the model API key |
| `MAGI_MODEL_API_KEY` | none | Key value, if no key file is set |
| `MAGI_MODEL_TIMEOUT` | `900` | The most time for one model call, in seconds |
| `MAGI_MODEL_FIRST_TIMEOUT` | `600` | The most time until the answer starts. Zen sends nothing until its answer is complete |
| `MAGI_MODEL_IDLE_TIMEOUT` | `90` | The most time between two chunks of a streamed answer. A provider that stops sending is retried after this time |
| `MAGI_MODEL_STREAM` | `true` | Stream the answers; `false` waits for the full answer (then the idle limit is the call limit) |
| `MAGI_MODEL_MAX_RETRIES` | `5` | Retries on 429, 5xx, connection errors, and idle or call timeouts |
| `MAGI_MODEL_BUSY_WAIT_S` | `1800` | The most time that one model call waits for a busy inference host. A host that stops its engine while the machine is busy answers 503 with `error.type` `host_busy` or `warming`. The apprentice waits the `Retry-After` seconds. It waits 30 s when the header is missing, and 120 s at most. These waits do not count as retries. The transcript records each wait as `model_wait` |
| `MAGI_MODEL_MAX_TOKENS` | `4096` | Output tokens for each reply |
| `MAGI_MODEL_TEMPERATURE` | `0.2` | Sampling temperature |
| `MAGI_WORKFLOW_URL` | none | WorkflowService base URL (worker mode) |
| `MAGI_EXECUTION_URL` | none | ExecutionService base URL |
| `MAGI_ARTIFACTS_URL` | none | ArtifactService base URL. If not set, no upload |
| `MAGI_TOKEN_FILE` | none | Bearer token file. The apprentice reads it again for each call |
| `MAGI_APPRENTICE_QUEUE` | `apprentice` | Workflow queue |
| `MAGI_APPRENTICE_WORKER_ID` | `apprentice-<host>-<pid>` | Worker name for claims |
| `MAGI_APPRENTICE_CHECK_COMMAND` | `./tools/check.sh` | Default check command (worker mode). Empty disables it |
| `MAGI_APPRENTICE_MAX_STEPS` | `60` | Model calls for each task |
| `MAGI_APPRENTICE_MAX_SECONDS` | `1800` | Wall-clock limit for each task |
| `MAGI_APPRENTICE_CONTEXT_TOKENS` | `24000` | Compaction threshold (estimate) |
| `MAGI_APPRENTICE_TOOL_OUTPUT_CHARS` | `8000` | Tool result size in the context |
| `MAGI_APPRENTICE_BASH_TIMEOUT` | `300` | Default bash timeout in seconds (maximum 1200) |
| `MAGI_APPRENTICE_PROGRESS_EVERY` | `5` | Tool calls between progress notes |
| `MAGI_APPRENTICE_ALIVE_EVERY` | `180` | Seconds with no progress note before an operation writes a "working:" note |
| `MAGI_APPRENTICE_CHECK_RETRIES` | `3` | Failed checks before `finish` is accepted |
| `MAGI_APPRENTICE_SELF_REVIEW` | `true` | One review request before the first accepted `finish`; `false` turns it off |
| `MAGI_APPRENTICE_TEST_GATE` | `true` | The new tests run once on the code without the change; `false` turns it off |
| `MAGI_APPRENTICE_SPEC_STEP` | `true` | With a review model: acceptance checks from the instructions before the work; `false` turns it off |
| `MAGI_APPRENTICE_TESTER` | `false` | With a review model: tests from the task text, run on the change before the work review |

For llama.cpp, start `llama-server` with `--jinja`. For a hosted provider,
change only the base URL, the model name, and the key file.

## Secrets

The transcript and the model context do not contain these values:

- The model API key and the Magi bearer token.
- The values of environment variables with `TOKEN`, `KEY`, `SECRET`,
  `PASSWORD`, `PASSWD`, or `CREDENTIAL` in the name.

The harness replaces each of these values with `[REDACTED]`. Values shorter
than 6 characters are not replaced.

## Image

```sh
docker build -f primitives/apprentice/Dockerfile -t magi-apprentice .
```

Build from the repository root. The image copies the generated clients from
`clients/python`. It runs as user 10001. The default command is `worker`.

## Tests

```sh
cd primitives/apprentice
python3 -m unittest discover -s tests          # or: .venv/bin/python -m pytest
```

The unit tests use a scripted model, an in-memory execution client, and a
fake workflow and artifact service behind the generated clients. One test
uses the real HTTP transport.

Live test with a real model. The tools run as local subprocesses in a
temporary directory:

```sh
MAGI_TEST_MODEL=1 MAGI_MODEL_BASE_URL=http://model.example.local/v1 \
MAGI_MODEL_API_KEY_FILE=/path/to/key python3 -m unittest discover -s tests -p test_live.py
```

## Known limits

- No streaming. A long reply waits for the full response.
- The environment cuts long output from the end. If a command writes more
  than 1 MiB, the end is lost; the tool result tells the model this.
- The token estimate is approximate before the first server response.
- The steering hook has no source in the workflow service yet.
- The apprentice does one task at a time. Run more workers for more parallel
  tasks.
- `--local` mode has no isolation. Use it only for tests.
