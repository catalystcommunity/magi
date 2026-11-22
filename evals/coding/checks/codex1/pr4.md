## Acceptance checks

Use `Apprentice.run` in `apprentice.py` for the loop checks. In `tests/test_apprentice.py`, use `apprentice` and `kinds`. Use `FakeModel`, `MemExecution`, `call`, `reply`, and `_res` from `tests/fakes.py`. Use the real `Tools` and `Workspace` from `tools.py`. Reuse these doubles.

1. "When the model calls finish for the first time and the check passed (or there is no check command), do not end yet."  
   Result: With no check, `finish("draft")` leads to a second model request. Repeat with `./check.sh` returning 0.  
   Seam: Inspect `FakeModel.requests`.

2. "Get `git diff --stat` and `git diff` of the work directory through the same Tools object (one exec; cut the diff to tool_output_chars)."  
   Result: Find exactly one request containing both commands, with `cwd="/work/repo"`. With `tool_output_chars=8000`, return 4,000 `A`s then 5,000 `B`s. Compare the diff portion to 3,200 `A`s, `\n... [1000 chars cut] ...\n`, and 4,800 `B`s.  
   Seam: Inspect `MemExecution.execs`. Set command-specific output with `_res`, following `test_check_gate_rejects_failed_check`.

3. "Reject that finish with a tool result that has the diff and this text:"  
   Result: Return `+x = 1\n` from the diff command. The next request contains that diff and this exact text:

   "Review your change before you finish, as a strict reviewer of this repository. Check each point and fix what is wrong: (1) Each part of the task instructions is done; (2) the project instructions are followed (generated files, tests, documentation style); (3) no change that the task did not ask for (renames, reformatting, unrelated edits); (4) no debug output, temporary files, or commented-out code; (5) tests for each new behavior, and they test the behavior, not the implementation; (6) names and comments are clear, and comments tell why. Then call finish again with the summary. If you change files, run the check again first."

   Seam: Compare `model.requests[1][-1]["content"]`. The matching transcript `tool_result` has `ok=False`.

4. "The second finish call ends the task as now (the check gate still applies to it)."  
   Result: Two passing finishes with summaries `draft`, `final` give `("finished", "final", 2)` for status, summary, and steps. With check exits `0, 1, 0`, the second finish shows `finish rejected: the check command failed.` and the third finishes.  
   Seam: Use `apprentice`, `FakeModel.requests`, and the existing check-gate `Exec` pattern.

5. "The review happens once in each run."  
   Result: The `0, 1, 0` run records one `self_review`. Run the same instance again with fresh scripted replies. Its transcript gains one more `self_review`.  
   Seam: Use `model.script` and `kinds(tr)`.

6. "A new limit self_review: bool = True in Limits (environment MAGI_APPRENTICE_SELF_REVIEW, "false" turns it off)."  
   Result: `Limits().self_review` is `True`. With the variable set to `false`, `Limits.from_env().self_review` is `False`.  
   Seam: Use `config.py` and `unittest.mock.patch.dict` on `os.environ`.

7. "The review does not happen when the step or time limit is near: when fewer than 5 model calls are left, or 80% of the time is used."  
   Result: First-call finish skips review with `max_steps=5` and reviews with `max_steps=6`. With `max_seconds=100` and ample steps, skip at 80 seconds and review at 79.  
   Seam: Use `apprentice(..., clock=...)`. Advance the clock from zero in a scripted reply. Compare `kinds(tr)`.

8. "Record the review in the transcript (kind "self_review", with the diff size in characters)."  
   Result: Stdout `+é\n` yields a `self_review` record with size 3.  
   Seam: Decode `tr.records` as in `kinds`.

9. "Tests with the FakeModel: the first finish is rejected with the diff and the review text; the second finish ends the task; self_review=False ends at the first finish; no review when fewer than 5 calls are left; a failed diff command still gives the review text (with "(no diff)")."  
   Result: Run checks 3, 4, and 7. Disabled review gives `("finished", "draft", 1)`. Diff exit 1 gives `(no diff)` and the complete review text.  
   Seam: Use `apprentice`, `FakeModel`, and `_res`.

10. "README.md (ASD-STE100): a short section "Self-review"."  
    Result: Match `## Self-review`. Read the section for short, active sentences.  
    Seam: `primitives/apprentice/README.md`.

11. Implied: Preserve check retries, redaction, and model accounting.  
    Result: With `check_retries=2`, exits `0, 1, 1` consume both failure attempts. Three replies total 300 prompt and 30 completion tokens. `SECRET` from diff stdout is absent from requests and transcript.  
    Seam: Use `reply`, `apprentice`, and `tr.records`.

## Plan

1. In `config.py`, add `self_review` to `Limits`. Use `_env` as for `MAGI_MODEL_STREAM`.
2. In `Apprentice.run`, track review locally. After `_finish_gate`, review eligible finishes with no check or a passing check. Use `res.steps` and `self.clock() - start`.
3. In `apprentice.py`, use `self.tools.ws.sh` once, following `repository_picture`. Use `as_text`, `truncate`, and `tr.record`. Preserve the full prompt, redaction, and tool-call pairing.
4. Add checks to `tests/test_apprentice.py` with `apprentice` and `tests/fakes.py` helpers. Disable review in existing fixtures that only test other behavior. Check `test_worker.py` and `test_start_context.py`.
5. Add `Self-review` after the finish rules in `README.md`. Run the requested Python suite, then `./tools/check.sh`.

Traps: Counting review as a check failure. Reviewing after exhausted retries. Cutting the prompt with the generic output cap. Measuring elapsed time before the finish check.