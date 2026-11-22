## Acceptance checks

1. “Fix: _shrink does not change the arguments of finish calls.”  
   Result: After the system and task messages, put `finish(summary="s" * 1978)` and `write_file(path="p", content="x" * 3000)` in one assistant message. Add their tool results and four later assistant/tool pairs. Call `compact` with `ctx.tokens() - 1`. In `ctx.wire()`, the finish arguments equal the original JSON string exactly. The write arguments decode to `{"path": "p", "content": "[compacted 3000 chars]"}`.  
   Seam: Use `Context.compact` in `primitives/apprentice/magi_apprentice/apprentice.py`. Follow `CompactionTest` in `primitives/apprentice/tests/test_apprentice.py`. Build the assistant message with `reply`, `call`, and `.message()`.

2. “Also, when the model calls finish with a summary that starts with "[compacted", reject that finish with a tool result that asks for the full summary (this does not count as a failed check).”  
   Result: With `check_retries=2` and self-review off, send `"[compacted 1978 chars]"`, then `"first try"`, then `"fixed"`. Make `./check.sh` return 1, then 0. The stub gets the new tool text `finish rejected: call finish again with the full summary.` The next finish gets the existing failed-check message. Only two `check` records exist, with `passed` values `[False, True]`. The run finishes with `"fixed"`.  
   Seam: Use `Apprentice.run`, `records`, and the execution pattern in `LoopTest.test_check_gate_rejects_failed_check`. Reuse `FakeModel`, `MemExecution`, `reply`, and `call` from `primitives/apprentice/tests/fakes.py`. Do not create replacement fakes.

3. “Tests: compaction keeps a finish summary; a stub summary is rejected and the next finish is accepted.”  
   Result: Use check 1 for compaction. With no check command and `Limits(self_review=False)`, send `"[compacted 1978 chars]"`, then `"Fixed the index."`. The second request ends with a `tool` message carrying the first call’s ID and the text from check 2. `(status, summary, steps, tool_calls)` equals `("finished", "Fixed the index.", 2, 2)`.  
   Seam: Use `apprentice` in `tests/test_apprentice.py`, `Apprentice.run(Task("t"))`, and `model.requests[1]`. Paths below are relative to `primitives/apprentice` unless stated otherwise.

4. “Fix: at the end of DIFF_SCRIPT, remove the intent-to-add entries again, so that the index is as it was before the review: git reset -q (the apprentice does not stage files; the workflow commits with git add -A).”  
   Result: With untracked `src/b.py` containing `y = 2\n`, the returned diff contains `+++ b/src/b.py` and `+y = 2`. Afterward, `git diff --cached --name-only` has empty stdout.  
   Seam: Call `workdir_diff(self.a.tools)` in `LocalStartContextTest` in `tests/test_start_context.py`.

5. “Test: the script ends with the reset after the diff commands.”  
   Result: The captured script runs intent-to-add, `git diff --stat`, then `git diff`. Its final command is `git reset -q`. Compare command lines so an `echo` heading cannot satisfy the check.  
   Seam: Extend `SelfReviewTest.test_diff_script_puts_new_files_in_the_diff` using `diff_exec()` and `e.execs` in `tests/test_apprentice.py`.

6. “Then test it against a real git repository with LocalExecution (see LocalStartContextTest for the pattern): after workdir_diff, a new file is still untracked (git status --short shows ??) and git stash -u works.”  
   Result: After check 4, `git status --short -- src/b.py` prints `?? src/b.py\n`. `git stash -u` exits 0. Status is then empty and `git rev-parse --verify refs/stash` exits 0.  
   Seam: Reuse `LocalStartContextTest.setUp`, its temporary repository, and `self.a.tools.ws.sh`.

7. Implied: Both model replies count.  
   Result: In check 3, usage totals are 200 prompt tokens and 20 completion tokens. There are two `model_response` and two `tool_result` records.  
   Seam: Use `reply` defaults and `records` from the loop tests.

## Plan

1. In `magi_apprentice/apprentice.py`, guard `_shrink_args` inside `Context._shrink` by each call’s function name.
2. At the start of `Apprentice._finish_gate`, test `summary.startswith("[compacted")`. Return the new text, `True`, `False`. This preserves the retry count and the normal tool-result path.
3. Append `git reset -q` to `DIFF_SCRIPT`. Correct its adjacent comment. Update one self-review line in `README.md` to mention the reset.
4. Add checks 1–3 and 7 in `tests/test_apprentice.py` using `CompactionTest`, `LoopTest`, and their helpers. Extend the script test there. Add checks 4 and 6 to `LocalStartContextTest`.
5. From the repository root, run `(cd primitives/apprentice && python3 -m unittest discover -s tests -q)`. Then run `./tools/check.sh`.

Traps: A finish inside the protected tail gives a false pass. Skipping the whole assistant message preserves sibling arguments. Returning `ok=False` spends a retry. Resetting before the patch hides new files.