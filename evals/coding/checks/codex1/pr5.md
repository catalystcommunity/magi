Acceptance checks

1. "SYSTEM_PROMPT in primitives/apprentice/magi_apprentice/apprentice.py: add one rule after the rule "Use the tools to inspect...": "Each model call costs money. When tool calls do not depend on each other, make them in one reply: for example, read three files, or read a file and search, in one reply. Read the files that the task names before you search.""
   - Result: The system message contains this exact rule once. It is the next rule after `Use the tools to inspect, change, build, and test. Do not guess file contents: read or search first.` It comes before the `Use edit_file` rule.
   - Seam: Use `build_prompt` in `apprentice.py` with `Task("Read a.py and b.py.")` and `Limits()`.

2. "In Apprentice.run, add the cost of each answer (also of the last call at a limit) to usage["cost"] as a float, next to prompt_tokens and completion_tokens." "Add the cached prompt tokens too, as usage["cached_tokens"], from usage.prompt_tokens_details.cached_tokens."
   - Result: Give two replies costs `0.01` and `0.02` and nested cached counts `7` and `11`. Expect `{"prompt_tokens": 200, "completion_tokens": 20, "cost": 0.03, "cached_tokens": 18}` with a float cost. Test a tool reply followed by `finish`, and a tool reply followed by a summary reply at `max_steps=1`. In the limit case expect `("step_limit", "Done.", 1)` for status, summary, and steps.
   - Seam: Use `Apprentice.run`, `apprentice` and `test_last_call_at_the_step_limit` in `primitives/apprentice/tests/test_apprentice.py` as the test pattern. Use `reply` and `call` from `tests/fakes.py`. Set usage on the returned replies. `apprentice` already uses `FakeModel`.

3. "A missing field counts as 0."
   - Result: Finish with a default `reply`. Expect `{"prompt_tokens": 100, "completion_tokens": 10, "cost": 0.0, "cached_tokens": 0}`. An empty `prompt_tokens_details` object also gives `0` cached tokens.
   - Seam: Use the same `Apprentice.run` and `apprentice` fixture.

4. "primitives/apprentice/magi_apprentice/__main__.py: the JSON result line also has "usage" (the dict above)."
   - Result: Decode stdout as JSON. For the two-reply run, compare `usage` to the four values in check 2.
   - Seam: `cmd_run` already prints `run_task(...).result.usage`. There is no CLI test helper.

5. "magi/runner/steps.go (work step): read usage from that JSON line." "Add the outputs apprentice.cost_usd (format %.3f) and apprentice.prompt_tokens, and add " (cost $X.XXX)" to the note "apprentice ... after N steps"." "When there is no cost, leave the outputs and the text out."
   - Result: Send `{"status":"finished","summary":"Done.","steps":2,"tool_calls":3,"check_passed":false,"usage":{"cost":0.0126,"prompt_tokens":200}}`. Expect `"0.013"` and `"200"` for the two outputs. Expect `apprentice finished after 2 steps (cost $0.013), 3 tool calls: Done.` Omit `usage`, omit only `cost`, or set `cost` to `0`. In each case both keys are absent and the note is `apprentice finished after 2 steps, 3 tool calls: Done.`
   - Seam: Call `Runner.work`. In `magi/runner/runner_test.go`, reuse `fakeMagi` and the HTTP setup in `TestCodeChangeOnRunner`. That test calls other step handlers directly. Extend `fakeMagi.Progress` to capture `req.Note`. Set `Cfg.Apprentice.Python` to a temporary executable that prints the fixture.

6. "Tests: a FakeModel test that two answers with cost 0.01 and 0.02 give usage["cost"] 0.03 and the cached tokens; a test in magi/runner for the note and the outputs with and without a cost (use the existing tests of the work step as the pattern)."
   - Result: The Python test compares the totals in check 2. The Go test compares the exact notes, output values, and absent keys in check 5.
   - Seam: Use the fixtures in checks 2 and 5. Reuse both existing fakes.

7. "README: one line in primitives/apprentice/README.md where the result is described."
   - Result: Add one line by `The command prints a JSON result.` Name `usage`, its token counts, and its cost in US dollars.
   - Seam: Inspect the CLI result paragraph in `primitives/apprentice/README.md`.

8. Implied: Keep usage in the transcript and preserve step counts.
   - Result: The limit case in check 2 has two `model_response` records. Each retains its original reply usage. The second has `last_call: true`. The `result` record has the aggregate usage and `steps: 1`.
   - Seam: Use `records` in `tests/test_apprentice.py`.

9. Implied: Preserve work outputs and errors.
   - Result: Check 5 still returns `done` with summary `Done.`, steps `"2"`, status `"finished"`, tool calls `"3"`, and check passed `"false"`. Change status to `"error"` and set error to `"provider failed"`. Expect `apprentice error: provider failed`.
   - Seam: Use the same `Runner.work` fixture.

Plan

1. In `apprentice.py`, insert the rule in `SYSTEM_PROMPT`. Leave the tool loop as it is.
2. Initialize both totals in `Apprentice.run`. Add one new usage accumulator shared by `run` and `_last_call`. Keep integer token conversion and use float conversion for cost.
3. Check `cmd_run` in `__main__.py`. It already includes `r.usage`.
4. In `steps.go`, add typed usage fields to the decode struct in `Runner.work`. When cost is nonzero, use `fmt.Sprintf` and `strconv.Itoa`. Insert the cost after `steps` in the existing `r.note` call.
5. Add the README line at the CLI result paragraph.
6. Add tests in `tests/test_apprentice.py` and `magi/runner/runner_test.go` with the helpers above. Run Python unittest discovery from `primitives/apprentice`, `GOWORK=off go test -count=1 ./runner/` from `magi`, then `./tools/check.sh` from the root.

Traps: Integer conversion drops fractional cost. Cached tokens are nested. The final limit reply adds usage without adding a step. The CLI prints one indented JSON document.