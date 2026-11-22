Acceptance checks

1. “Fix: _shrink does not change the arguments of finish calls.”
   - Result: Put an old `finish` with a summary of `"s" * 1978` before an old `write_file` with `"x" * 12000` as its content. Give each call a tool result. Follow them with four short call/result pairs. Run `compact` with a budget of `ctx.tokens() - 1000`. The old `finish` arguments equal the original JSON string, character for character.
   - Seam: Use `Context.compact` and `Context.wire` in `magi_apprentice/apprentice.py`. Follow `CompactionTest` in `tests/test_apprentice.py`. Paths below are relative to `primitives/apprentice`.

2. “Also, when the model calls finish with a summary that starts with "[compacted", reject that finish with a tool result that asks for the full summary (this does not count as a failed check).”
   - Result: Send `[compacted 1978 chars]`, `First full summary`, and `Final full summary`. Set `check_command="false"`, `check_retries=2`, and `self_review=False`. The second model request has the tool result `finish rejected: provide the full summary and call finish again.` The third request has `finish rejected: the check command failed.\nexit_code: 1\n(no output)\nFix the problem, then call finish again.` There are exactly two `check` records, both with `passed=False`. The final result is `("finished", "Final full summary", False)` for status, summary, and check result.
   - Seam: Use `Apprentice.run`, `FakeModel`, `call`, and `reply` from `tests/fakes.py` for model replies, and `records` from `tests/test_apprentice.py` for the transcript. Use `Tools(Workspace(...))` with `LocalExecution` from `magi_apprentice/local.py` for `false`.

3. “Tests: compaction keeps a finish summary; a stub summary is rejected and the next finish is accepted.”
   - Result: Use check 1 for compaction. With self-review off and no check command, send `[compacted 1978 chars]`, then `Updated x.py.` The second request ends with the rejection text from check 2. The result has status `finished`, summary `Updated x.py.`, and exactly two steps and two tool calls.
   - Seam: Use `apprentice` in `tests/test_apprentice.py` and `call` and `reply` in `tests/fakes.py`. Use the existing fakes throughout.

4. “Fix: at the end of DIFF_SCRIPT, remove the intent-to-add entries again, so that the index is as it was before the review: git reset -q (the apprentice does not stage files; the workflow commits with git add -A).”
   - Result: In the captured review script, `git add --intent-to-add --all 2>/dev/null` precedes `git diff --stat 2>/dev/null`, which precedes `git diff 2>/dev/null`. The last line is `git reset -q 2>/dev/null`.
   - Seam: Extend `SelfReviewTest.test_diff_script_puts_new_files_in_the_diff` in `tests/test_apprentice.py`. Use `diff_exec()` and inspect `e.execs` after `Apprentice.run`.

5. “Test: the script ends with the reset after the diff commands.”
   - Result: Use the line order and exact final line in check 4.
   - Seam: Use the same test and captured script.

6. “Then test it against a real git repository with LocalExecution (see LocalStartContextTest for the pattern): after workdir_diff, a new file is still untracked (git status --short shows ??) and git stash -u works.”
   - Result: Commit `AGENTS.md` in a temporary repository. Create `new.py` with `x = 1\n` as the only change. After `workdir_diff`, `git status --short` returns exactly `?? new.py\n`. `git stash -u` exits with code `0`, removes `new.py` from disk, and creates `refs/stash`.
   - Seam: Use `workdir_diff` and `LocalExecution` with `Tools` and `Workspace`. Follow `LocalStartContextTest.setUp` in `tests/test_start_context.py`.

7. Implied: Keep shrinking other old calls and showing new files in the review.
   - Result: In check 1, the decoded `write_file` content is `[compacted 12000 chars]`. In check 6, the returned diff contains `+++ b/new.py\n` and `+x = 1\n`.
   - Seam: Use the same context and real repository tests.

8. Implied: Keep model usage and tool records for both finish attempts.
   - Result: In check 3, default `reply` usage totals 200 prompt tokens and 20 completion tokens. There are two `model_response` records. The first `tool_result` has `ok=False` and the rejection text. The second has `ok=True` and text `finished`.
   - Seam: Use `records` and `apprentice` in `tests/test_apprentice.py`.

Plan

1. In `magi_apprentice/apprentice.py`, guard `_shrink_args` in `Context._shrink` by the tool function name `finish`.
2. In `Apprentice.run`, check `r.content.startswith("[compacted")` when `r.finished` is true. Set the rejection text and `ok=False`. Keep `_finish_gate`, `_self_review`, and `check_fails` in the other branch. Use the common tool result path.
3. Append the reset to `DIFF_SCRIPT`. Update its comment and add one line in `README.md` under Self-review to say the script resets the index after the diff.
4. Add the tests in `tests/test_apprentice.py` with `CompactionTest`, `apprentice`, `records`, and `diff_exec`. Use the `LocalStartContextTest` setup pattern for the real Git tests. Run `python3 -m unittest discover -s tests -q` from `primitives/apprentice`. Then run `./tools/check.sh` from the repository root.

Traps: A tail call does not exercise old argument shrinking. A passing check cannot expose a spent retry. Stash can exit zero without creating an entry. Reset before the diff hides new files.