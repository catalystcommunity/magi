Acceptance checks

1. “Give the coding harness the project instructions and a picture of the repository at the start of each task, the way OpenCode and Pi do.” Result: Run two tasks with different `AGENTS.md` text, `Rule A` and `Rule B`. Each first user message contains its own rule and `Repository:`. Seam: `Apprentice.run` in `magi_apprentice/apprentice.py`. Below, “loop” means `apprentice` in `tests/test_apprentice.py` with `FakeModel`, `MemExecution`, `reply`, and `call` from `tests/fakes.py`. All paths are under `primitives/apprentice/`.
2. “Before the first model call, read AGENTS.md in the work directory.” Result: With `AGENTS.md` containing `Use Go.` and `CLAUDE.md` containing `Use Rust.`, the first request contains `Use Go.` only. Seam: loop, `model.requests[0]`.
3. “If there is no AGENTS.md, read CLAUDE.md.” Result: With only `CLAUDE.md`, the user message contains `Project instructions (from CLAUDE.md):\nUse Rust.` Seam: loop, `MemExecution.files`.
4. “Put the text in the first user message under the heading "Project instructions (from AGENTS.md):" (or CLAUDE.md), before the task instructions.” Result: For `Task("Fix x")`, message 1 has role `user`. `Project instructions (from AGENTS.md):\nUse Go.` occurs before `Task instructions:\nFix x`. Seam: loop, `model.requests[0][1]`.
5. “Cut it at 20000 characters with a note.” Result: For `"é"*20000 + "Z"*5000`, expect `"é"*20000 + "\n... [5000 chars cut] ..."` using the existing note style. Exactly 20000 characters need no note. Seam: loop.
6. “A missing file is not an error.” Result: With neither file, the user message has no project-instructions heading. A scripted `finish` returns `finished`. Seam: loop.
7. “Read the file through the same Tools object that the loop uses (the files are in the sandbox; the harness reaches them only through the execution service), not with open().” Result: Seed only `/work/repo/AGENTS.md` in `MemExecution` with `Sandbox rule.`. That exact text reaches the model. Seam: loop, `a.tools.ws`.
8. “Also add a section "Repository:" with: the output of `git status --short` (at most 50 lines), `git log --oneline -5`, and the top-level entries of the work directory (one line, sorted).” Result: In a temporary repository with six commits, modify tracked `pkg/f01` through `pkg/f51`. Expect exactly ` M pkg/f01` through ` M pkg/f50`, including the leading spaces. Compare history with independent `git log --oneline -5` output. With empty directories `z` and `a`, expect `.git a pkg z` on one line. Seam: `runner.run_task`, `MemExecution(local=LocalExecution())` from `tests/fakes.py`.
9. “Get them with one exec through the same Tools object.” Result: Script `reply(content="continue")` then `reply(call("finish", summary="ok"))` without a check command. `len(m.execs)` is 1 across both requests. Its `cwd` equals `Task.workdir`. Seam: same `run_task` fixture.
10. “If a command fails, leave that part out.” Result: In a freshly initialized repository with file `x`, retain `?? x` and `.git x`. Omit the log part and Git diagnostic. Seam: same fixture.
11. “Add a line to SYSTEM_PROMPT that says that the project instructions are rules of the repository owner and that the agent must follow them, unless the task instructions say otherwise.” Result: Message 0 contains `Follow the repository owner's project instructions unless the task instructions say otherwise.` Seam: loop.
12. “Tests in primitives/apprentice/tests: with AGENTS.md, with only CLAUDE.md, with neither, with a long file (cut), and a failed git command.” Result: Checks 2, 3, 6, 5, and 10 pass. Seam: `tests/test_apprentice.py`.
13. “Use the fakes that the existing tests use for Tools.” Result: Real `Tools(Workspace(...))` receives the existing execution fake. Seam: loop.
14. “Update primitives/apprentice/README.md (ASD-STE100 style) with a short section "Context at the start".” Result: `## Context at the start` introduces short, active sentences. Seam: README inspection.
15. Implied: Preserve redaction and accounting. Result: `SECRET` in a rule becomes `[REDACTED]` in the request and transcript. One scripted finish gives one step, one tool call, 100 prompt tokens and 10 completion tokens. Seam: loop, `tr.records`.

Plan

1. In `apprentice.py`, collect context once in `Apprentice.run` before `build_prompt`. Use `self.tools.ws.path`, `read`, and `ToolError` for fallback.
2. Use one `ws.sh`. Capture each command independently. Emit marked successful parts. Preserve status whitespace.
3. Pass redacted sections into `build_prompt`. Keep existing `Task` fields and prompt sections. Extend `SYSTEM_PROMPT`. `runner.py` already supplies the shared `Tools`.
4. Add the checks in `tests/test_apprentice.py` with the named helpers. Reuse `LocalExecution` for shell behavior.
5. Update `README.md`. Run `python3 -m unittest discover -s tests -q` from `primitives/apprentice`, then `./tools/check.sh` from the root.

Traps: `read_file` adds line numbers. `truncate` keeps the tail. `.strip()` destroys Git’s first status column. `MemExecution` alone returns empty shell output.