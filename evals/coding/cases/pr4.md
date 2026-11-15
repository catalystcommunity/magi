# Apprentice: self-review of the diff before finish

Add a self-review step to the apprentice: before the task ends, the model reads its own diff against the task and the project instructions, and fixes what a reviewer would send back.

Files: primitives/apprentice/magi_apprentice/apprentice.py (the finish handling in the run loop), primitives/apprentice/magi_apprentice/config.py (Limits), primitives/apprentice/tests/, primitives/apprentice/README.md.

1. When the model calls finish for the first time and the check passed (or there is no check command), do not end yet. Get `git diff --stat` and `git diff` of the work directory through the same Tools object (one exec; cut the diff to tool_output_chars). Reject that finish with a tool result that has the diff and this text:
   "Review your change before you finish, as a strict reviewer of this repository. Check each point and fix what is wrong: (1) Each part of the task instructions is done; (2) the project instructions are followed (generated files, tests, documentation style); (3) no change that the task did not ask for (renames, reformatting, unrelated edits); (4) no debug output, temporary files, or commented-out code; (5) tests for each new behavior, and they test the behavior, not the implementation; (6) names and comments are clear, and comments tell why. Then call finish again with the summary. If you change files, run the check again first."
2. The second finish call ends the task as now (the check gate still applies to it). The review happens once in each run.
3. A new limit self_review: bool = True in Limits (environment MAGI_APPRENTICE_SELF_REVIEW, "false" turns it off). The review does not happen when the step or time limit is near: when fewer than 5 model calls are left, or 80% of the time is used.
4. Record the review in the transcript (kind "self_review", with the diff size in characters).
5. Tests with the FakeModel: the first finish is rejected with the diff and the review text; the second finish ends the task; self_review=False ends at the first finish; no review when fewer than 5 calls are left; a failed diff command still gives the review text (with "(no diff)").
6. README.md (ASD-STE100): a short section "Self-review".

Keep the change small. Do not change the tools or the model client.
Verify: cd primitives/apprentice && python3 -m unittest discover -s tests -q. Then ./tools/check.sh.
