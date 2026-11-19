1. "Before the first model call, read AGENTS.md in the work directory. Put the text in the first user message under the heading "Project instructions (from AGENTS.md):" (or CLAUDE.md), before the task instructions." Result: work directory contains AGENTS.md with content "Follow these rules"; build_prompt returns a message list where the first user message starts with the exact heading "Project instructions (from AGENTS.md):" followed by the file content, placed before the task instructions string. Seam: apprentice.build_prompt with the test's fake Tools.

2. "If there is no AGENTS.md, read CLAUDE.md." Result: work directory contains only CLAUDE.md with content "Fallback rules"; build_prompt returns a first user message containing the exact heading "Project instructions (from CLAUDE.md):" followed by the file content. Seam: apprentice.build_prompt with the test's fake Tools.

3. "Cut it at 20000 characters with a note." Result: work directory contains AGENTS.md with 25000 characters; build_prompt returns a first user message where the file content is truncated to exactly 20000 characters and includes a note indicating the cut. Seam: apprentice.build_prompt with the test's fake Tools.

4. "A missing file is not an error." Result: work directory contains neither AGENTS.md nor CLAUDE.md; build_prompt completes without raising an exception and the first user message contains no project instructions section. Seam: apprentice.build_prompt with the test's fake Tools.

5. "Read the file through the same Tools object that the loop uses (the files are in the sandbox; the harness reaches them only through the execution service), not with open()." Result: the fake Tools object records a call to its file reading method for the project file, and Python's built-in open is not invoked during prompt construction. Seam: apprentice.build_prompt using the test's fake Tools.

6. "Also add a section "Repository:" with: the output of `git status --short` (at most 50 lines), `git log --oneline -5`, and the top-level entries of the work directory (one line, sorted)." Result: work directory is a git repo with status output " M file1\n M file2", log output "abc1234 Initial commit", and top-level entries "dir1 file2.py"; build_prompt returns a user message containing the exact heading "Repository:" followed by the git status output (truncated to 50 lines if needed), the git log output, and a sorted one-line list of top-level entries. Seam: apprentice.build_prompt with the test's fake Tools.

7. "Get them with one exec through the same Tools object." Result: the fake Tools object records exactly one call to its exec/run method to retrieve the repository picture data. Seam: apprentice.build_prompt using the test's fake Tools.

8. "If a command fails, leave that part out." Result: `git status --short` returns an error via the fake Tools; build_prompt returns a user message containing the "Repository:" heading with only the `git log` and directory entries sections, omitting the failed status section entirely. Seam: apprentice.build_prompt with the test's fake Tools.

9. "Add a line to SYSTEM_PROMPT that says that the project instructions are rules of the repository owner and that the agent must follow them, unless the task instructions say otherwise." Result: SYSTEM_PROMPT contains a line stating that project instructions are rules of the repository owner and must be followed unless task instructions say otherwise. Seam: Direct inspection of apprentice.SYSTEM_PROMPT.

10. "Tests in primitives/apprentice/tests: with AGENTS.md, with only CLAUDE.md, with neither, with a long file (cut), and a failed git command." Result: `python3 -m unittest discover -s tests -q` passes, and the test suite includes distinct test methods verifying each of the five scenarios (AGENTS.md present, CLAUDE.md only, neither present, long file truncated, git command failure). Seam: unittest runner in primitives/apprentice/tests.

11. "Use the fakes that the existing tests use for Tools." Result: The new tests instantiate and use the same fake Tools class defined in the existing test fixtures. Seam: primitives/apprentice/tests test module.

12. "Update primitives/apprentice/README.md (ASD-STE100 style) with a short section "Context at the start"." Result: primitives/apprentice/README.md contains a section titled "Context at the start" written in ASD-STE100 style (short sentences, active voice, no complex words). Seam: File inspection of primitives/apprentice/README.md.
