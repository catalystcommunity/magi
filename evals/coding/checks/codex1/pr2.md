## Acceptance checks

1. "Before the first model call, read AGENTS.md in the work directory."  
   Result: Put `Use tabs.` in `/work/repo/AGENTS.md` and `Use spaces.` in `/work/repo/CLAUDE.md`. The first model request contains `Use tabs.` and omits `Use spaces.`  
   Seam: `Apprentice.run` in `magi_apprentice/apprentice.py`. Use `apprentice` in `tests/test_apprentice.py` and inspect `FakeModel.requests` from `tests/fakes.py`.

2. "If there is no AGENTS.md, read CLAUDE.md."  
   Result: Supply only `/work/repo/CLAUDE.md` with `Use spaces.`. The first user message contains `Project instructions (from CLAUDE.md):\nUse spaces.`  
   Seam: Use the same `apprentice` helper and inspect `model.requests[0][1]`.

3. "Put the text in the first user message under the heading "Project instructions (from AGENTS.md):" (or CLAUDE.md), before the task instructions."  
   Result: With `AGENTS.md` text `Use tabs.\nKeep tests.` and `Task("Fix x")`, message 1 has role `user` and contains `Project instructions (from AGENTS.md):\nUse tabs.\nKeep tests.\n\nTask instructions:\nFix x`.  
   Seam: Use `apprentice` and `FakeModel.requests`.

4. "Cut it at 20000 characters with a note."  
   Result: Supply 20,000 `é` characters followed by 5,000 `Z` characters. Compare the project text with exactly 20,000 `é` characters followed by `\n... [5000 chars cut] ...`.  
   Seam: Use `apprentice` with the file in `MemExecution`. Compare the project section in the first request.

5. "A missing file is not an error."  
   Result: With neither file and a scripted `finish` summary of `s`, the result is `("finished", "s")` for status and summary. The first user message contains `Task instructions:\nFix x` and no project instructions heading.  
   Seam: Use `apprentice`, `reply`, and `call` from the existing tests.

6. "Read the file through the same Tools object that the loop uses (the files are in the sandbox; the harness reaches them only through the execution service), not with open()."  
   Result: Store `Sandbox only.` solely in `MemExecution.files`. The first prompt includes it. A later model `read_file` call for `AGENTS.md` returns `     1\tSandbox only.`  
   Seam: Use `apprentice` with real `Tools` and `Workspace`. Script the call with `call` and `reply`. Reuse `MemExecution` and `FakeModel`. Do not make replacement fakes.

7. "Also add a section "Repository:" with: the output of `git status --short` (at most 50 lines), `git log --oneline -5`, and the top-level entries of the work directory (one line, sorted)."  
   Result: Supply 51 status lines `?? f01` through `?? f51`. Compare the displayed lines with `?? f01` through `?? f50`. Compare the log with the five supplied lines `a000006 six` through `a000002 two`, newest first. Supply entries `z` and `a` in reverse order. Compare the listing line with `a z`.  
   Seam: Inspect `FakeModel.requests[0][1]` after `Apprentice.run`. Use `MemExecution` with the `exec` override pattern in `LoopTest.test_check_gate_rejects_failed_check`.

8. "Get them with one exec through the same Tools object."  
   Result: With `Task("Fix x", workdir="/sandbox/project")` and a single `finish` reply, `MemExecution.execs` has one request with `cwd == "/sandbox/project"`. Its script contains both specified git commands and the directory listing command.  
   Seam: Use `runner.run_task` with `MemExecution` and `FakeModel`, like the `run_task` call in `tests/test_live.py`.

9. "If a command fails, leave that part out."  
   Result: Make status print `BAD STATUS` and exit 1. Let log return `ab12345 Init` and the listing return `a z`. Under `Repository:`, keep both successful outputs. Omit `BAD STATUS` and the status error text.  
   Seam: Use `Apprentice.run` and `MemExecution(local=LocalExecution())` with controlled command results. Inspect the first `FakeModel` request.

10. "Add a line to SYSTEM_PROMPT that says that the project instructions are rules of the repository owner and that the agent must follow them, unless the task instructions say otherwise."  
    Result: Message 0 contains `Follow the repository owner's project instructions unless the task instructions say otherwise.`  
    Seam: Inspect `FakeModel.requests[0][0]` through `apprentice`.

11. "Update primitives/apprentice/README.md (ASD-STE100 style) with a short section "Context at the start"."  
    Result: `## Context at the start` describes file priority, the 20,000-character limit, and the repository picture in short, active sentences.  
    Seam: Review `primitives/apprentice/README.md`.

12. Implied: Startup preserves loop accounting and the transcript.  
    Result: Two scripted replies for `write_file` and `finish` give two steps, two tool calls, usage of 200 prompt tokens and 20 completion tokens, and two each of `model_request` and `model_response`. The first request record contains the startup context.  
    Seam: Extend `LoopTest.test_write_then_finish`. Use `kinds` and `tr.records`.

## Plan

1. In `apprentice.py`, gather context once in `Apprentice.run` from `self.tools.ws`. Use `Workspace.path` and `Workspace.read` for raw text. Handle a missing file and try the fallback. Apply `Transcript.redactor.text` to startup text before it enters model context.
2. In the same startup path, use one `Workspace.sh` call. Frame the successful outputs separately. Check each exit status before including its output. Cap status by lines and sort the top-level names.
3. In `apprentice.py`, pass the startup text to `build_prompt` with new optional parameters. Add the sections to the first user message. Update `SYSTEM_PROMPT`. Keep `runner.run_task` as the shared caller that supplies `Tools`.
4. In `tests/test_apprentice.py`, add all five requested cases with `apprentice`, `MemExecution`, `FakeModel`, `call`, and `reply`. Add the README section. Run `cd primitives/apprentice && python3 -m unittest discover -s tests -q`, then run `./tools/check.sh` from the root.

Traps: `Tools.read_file` adds line numbers. `truncate` keeps the head and tail. A shell pipeline can hide git’s exit status. `MemExecution.exec` returns empty output by default.