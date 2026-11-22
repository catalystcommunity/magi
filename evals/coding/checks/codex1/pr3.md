Acceptance checks

1. “When 80% of max_steps are used, and again when 5 steps are left, add a user message before the next model call: "Budget: N of M model calls are left (and S seconds of the time limit). Finish the work that is necessary, run the check, and call finish with a summary. Do not start new improvements."”  
   Result: Set `Limits(max_steps=30, max_seconds=100)` and use `clock=lambda: 0`. Script 26 `bash` replies and then `finish`. Calls 25 and 26 must each end with a user message that matches the quoted template exactly. Substitute `(N, M, S)` with `(6, 30, 100)` and `(5, 30, 100)`, respectively.  
   Seam: `Apprentice.run`, `apprentice` in `tests/test_apprentice.py`, and `FakeModel.requests`, `call`, and `reply` in `tests/fakes.py`. Reuse these helpers.

2. “Do the same for max_seconds at 80% (one note).”  
   Result: Set limits to 30 calls and 100 seconds. Supply clock readings `0, 0, 79, 80, 81` starting with the start time. Script three `bash` replies and then `finish`. The note first appears as the last user message on call 3 with `(28, 30, 20)`.  
   Seam: Pass `clock` through `apprentice` in `tests/test_apprentice.py`. Inspect `FakeModel.requests`.

3. “Each note goes in once.”  
   Result: Count user messages that match the warning template in the last request. Expect two distinct warnings in check 1 and one in check 2, each with one occurrence.  
   Seam: Use the same `FakeModel.requests` snapshots.

4. “Record each note in the transcript (kind "budget_note").”  
   Result: Filter `tr.records` for `budget_note`. Compare the full warning texts in order to the two messages in check 1 and the one in check 2.  
   Seam: Use the transcript returned by `apprentice`. Follow `kinds` in `tests/test_apprentice.py` to decode records.

5. “When the step limit or the time limit stops the loop and the model did not call finish, make one more model call with no tools: a user message that asks for the finish summary (what changed, how it was verified, what is left undone).”  
   Result: With three allowed calls and three `bash` replies, expect four requests. With 100 seconds and clock readings `0, 0, 101`, expect two requests after one `bash` reply. In both cases compare the final tool list to `[]` and the last message to `{"role": "user", "content": "Give the finish summary: what changed, how it was verified, and what is left undone."}`.  
   Seam: Update `test_step_limit` and `test_time_limit` in `tests/test_apprentice.py`. Spy on the existing `FakeModel.complete` to capture tools.

6. “Put that text in Result.summary, and keep the status step_limit or time_limit.”  
   Result: End both scripts with `reply(content="Changed x. Ran checks. Review remains.")`. Compare `summary` to that exact string and `status` to the applicable limit status.  
   Seam: Inspect the result from `Apprentice.run` in both limit tests.

7. “If that call fails, keep the current summary text.”  
   Result: After three allowed calls, make the next script item raise `ModelError("context too long")`. Expect four requests, status `step_limit`, and summary `Stopped after 3 model calls (step limit).`  
   Seam: Use a callable script item in `FakeModel` from `tests/fakes.py`.

8. “The progress counts in the first user message: add a line "Budget: M model calls, S seconds." after the work directory line.”  
   Result: With limits 30 and 100, compare consecutive prompt sections to `Work directory: /work/repo\n\nBudget: 30 model calls, 100 seconds.`  
   Seam: Use `apprentice` and `first_user` in `tests/test_start_context.py`.

9. “Tests in primitives/apprentice/tests with the FakeModel: the two step notes (once each, at the right steps), the time note (use the clock that Apprentice takes), the last call at the step limit (summary text from the model, status step_limit), a failed last call, and no notes in a short task.”  
   Result: Cover checks 1–7. With default limits, a fixed clock, and immediate `finish`, expect one request and zero `budget_note` records.  
   Seam: Use `apprentice`, `kinds`, and `LoopTest` in `tests/test_apprentice.py`.

10. “Update primitives/apprentice/README.md (ASD-STE100) where it explains the limits.”  
    Result: Beside the two limit settings, document the three warning thresholds and one summary call without tools at either limit. Use short, active sentences.  
    Seam: Review the `MAGI_APPRENTICE_MAX_STEPS` and `MAGI_APPRENTICE_MAX_SECONDS` rows and adjacent text.

11. Implied: Three normal replies and one summary reply give `steps=3`, `tool_calls=3`, and usage `{"prompt_tokens": 400, "completion_tokens": 40}`. The final request and response records precede `result`.  
    Seam: Use `reply`, `apprentice`, and `kinds`.

Plan

1. In `apprentice.py`, insert the line in `build_prompt` using `Limits.max_steps` and `Limits.max_seconds`.
2. In `Apprentice.run`, reuse `self.clock()` for elapsed time. Track each warning separately. Append due warnings before normal requests and use `tr.record`.
3. In `Apprentice.run`, route both limit exits through one summary path. Use `ctx.wire()`, `self.model.complete` with `[]`, existing usage and transcript fields, and `done`. Catch `ModelError` and retain the original limit text.
4. Add the checks in `tests/test_apprentice.py` and `tests/test_start_context.py` using the helpers above.
5. Update the limits text in `README.md`. Run `python3 -m unittest discover -s tests -q` from `primitives/apprentice`, then `./tools/check.sh` from the root.

Traps: Count completed calls before sending warnings. Keep both triggers if they coincide. `_complete` supplies tools and can retry. Keep check-gate behavior for normal `finish` calls.