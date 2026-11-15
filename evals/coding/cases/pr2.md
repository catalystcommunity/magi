# Apprentice: project instructions and repository picture at start

Give the coding harness the project instructions and a picture of the repository at the start of each task, the way OpenCode and Pi do.

Files: primitives/apprentice/magi_apprentice/apprentice.py (build_prompt, SYSTEM_PROMPT, Task), primitives/apprentice/magi_apprentice/runner.py (where Task is built and the loop starts), primitives/apprentice/tests/.

1. Project instructions. Before the first model call, read AGENTS.md in the work directory. If there is no AGENTS.md, read CLAUDE.md. Put the text in the first user message under the heading "Project instructions (from AGENTS.md):" (or CLAUDE.md), before the task instructions. Cut it at 20000 characters with a note. A missing file is not an error. Read the file through the same Tools object that the loop uses (the files are in the sandbox; the harness reaches them only through the execution service), not with open().
2. Repository picture. Also add a section "Repository:" with: the output of `git status --short` (at most 50 lines), `git log --oneline -5`, and the top-level entries of the work directory (one line, sorted). Get them with one exec through the same Tools object. If a command fails, leave that part out.
3. Add a line to SYSTEM_PROMPT that says that the project instructions are rules of the repository owner and that the agent must follow them, unless the task instructions say otherwise.
4. Tests in primitives/apprentice/tests: with AGENTS.md, with only CLAUDE.md, with neither, with a long file (cut), and a failed git command. Use the fakes that the existing tests use for Tools.
5. Update primitives/apprentice/README.md (ASD-STE100 style) with a short section "Context at the start".

Verify: cd primitives/apprentice && python3 -m unittest discover -s tests -q. Then ./tools/check.sh.
