## Acceptance checks

1. “When 80% of max_steps are used, and again when 5 steps are left, add a user message before the next model call: "Budget: N of M model calls are left (and S seconds of the time limit). Finish the work that is necessary, run the check, and call finish with a summary. Do not start new improvements."”  
   Result: Set `M=41`, `S=100`, and hold the clock fixed. Number requests from 1. Request 34 ends with the quoted message, role `user`, with `N=8`. Request 37 ends with it with `N=5`. Compare the full text.  
   Seam: Run `Apprentice.run(Task("t"))`. Use `apprentice` in `primitives/apprentice/tests/test_apprentice.py` and `FakeModel`, `call`, `reply` in adjacent `fakes.py` throughout. Use these helpers instead of a new fake. Script `bash true` replies to advance the loop.

2. “Do the same for max_seconds at 80% (one note).”  
   Result: With limits 41 and 100, start the clock at 100. Advance it to 179, 180, then 181 after successive replies. Request 2 has no warning. Request 3 ends with the exact warning from check 1 with `N=39`, `M=41`, `S=20`. Request 4 still contains just one warning.  
   Seam: Pass `clock=` through `apprentice`. Use callable `FakeModel` script items to advance it.

3. “Each note goes in once.”  
   Result: Finish on request 40 in check 1. Cumulative warning counts on requests 33, 34, 36, 37, 40 are `0,1,1,2,2`.  
   Seam: Inspect `model.requests`. Count standalone user warnings.

4. “Record each note in the transcript (kind "budget_note").”  
   Result: Check 1 produces two `budget_note` records with the two exact messages in order. Check 2 produces one.  
   Seam: Use `kinds(tr)` and decode `tr.records` with `json.loads`.

5. “When the step limit or the time limit stops the loop and the model did not call finish, make one more model call with no tools: a user message that asks for the finish summary (what changed, how it was verified, what is left undone).”  
   Result: After three `bash true` replies at `max_steps=3`, request 4 contains the preceding tool result and ends with that three-part user request. Its tools argument is `[]`. There are exactly four requests.  
   Seam: Extend `LoopTest.test_step_limit`. Wrap `model.complete` to inspect its arguments. `model.requests` stores only messages.

6. “Put that text in Result.summary, and keep the status step_limit or time_limit.”  
   Result: For `reply(content="Fixed x. Tests passed. Nothing remains.")`, expect that exact summary with `step_limit` in check 5 and `time_limit` after the clock exceeds 2500 in `LoopTest.test_time_limit`.  
   Seam: Inspect the result of `Apprentice.run`.

7. “If that call fails, keep the current summary text.”  
   Result: Expect `Stopped after 3 model calls (step limit).` or `Stopped after 2500 s (time limit).` with the corresponding limit status.  
   Seam: Script a final callable that raises `ModelError("offline")`.

8. “The progress counts in the first user message: add a line "Budget: M model calls, S seconds." after the work directory line.”  
   Result: With limits 41 and 100, successive nonblank lines are `Work directory: /work/repo`, `Budget: 41 model calls, 100 seconds.`, `Repository:`.  
   Seam: Extend `BuildPromptTest.test_order_of_sections` in `tests/test_start_context.py` using `build_prompt`.

9. “Tests in primitives/apprentice/tests with the FakeModel: the two step notes (once each, at the right steps), the time note (use the clock that Apprentice takes), the last call at the step limit (summary text from the model, status step_limit), a failed last call, and no notes in a short task.”  
   Result: With default limits, first-call `finish(summary="done")` gives `finished`, `done`, one request and zero warnings.  
   Seam: `LoopTest`, `apprentice`, `kinds`.

10. “Update primitives/apprentice/README.md (ASD-STE100) where it explains the limits.”  
    Result: Configuration explains all three warnings and the extra summary call at either limit.  
    Seam: Inspect the two `MAGI_APPRENTICE_MAX_` limit rows and nearby prose.

11. Implied: Result: Check 5 reports three steps, three tool calls, 400 prompt tokens and 40 completion tokens. Four `model_request`/`model_response` pairs precede `result`. Failure records `model_error`.  
    Seam: `reply`, `kinds(tr)`, `Result.usage`.

## Plan

1. In `magi_apprentice/apprentice.py`, insert the budget in `build_prompt` using `config.py`’s existing `Limits`.
2. In `Apprentice.run`, sample `self.clock()` once per iteration. Preserve the limit guards. Before incrementing `res.steps`, append and record each newly due warning. Track three independent flags per run.
3. Share request logging and usage accounting. At either limit, send `ctx.wire()` with the summary request and `[]`. Handle `ModelError` and return through `done`. Keep `_complete`’s overflow retry for normal calls.
4. Add the checks above in `tests/test_apprentice.py` and `tests/test_start_context.py` with the named helpers.
5. Update `README.md`. Run `python3 -m unittest discover -s tests -q` from `primitives/apprentice`, then `./tools/check.sh` from the root.

Traps: Rounding 80% down. Counting old warnings again. Passing `TOOL_SPECS` on the final call. Charging the extra call to `res.steps`.