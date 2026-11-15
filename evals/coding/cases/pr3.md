# Apprentice: step and time budget notes, and a summary at a limit

Make the apprentice aware of its step and time budget, so that it finishes with a summary instead of stopping at a limit with no summary.

Files: primitives/apprentice/magi_apprentice/apprentice.py (the run loop, Limits use), primitives/apprentice/magi_apprentice/config.py (Limits), primitives/apprentice/tests/.

1. Budget notes. When 80% of max_steps are used, and again when 5 steps are left, add a user message before the next model call: "Budget: N of M model calls are left (and S seconds of the time limit). Finish the work that is necessary, run the check, and call finish with a summary. Do not start new improvements." Do the same for max_seconds at 80% (one note). Each note goes in once. Record each note in the transcript (kind "budget_note").
2. Last call. When the step limit or the time limit stops the loop and the model did not call finish, make one more model call with no tools: a user message that asks for the finish summary (what changed, how it was verified, what is left undone). Put that text in Result.summary, and keep the status step_limit or time_limit. If that call fails, keep the current summary text.
3. The progress counts in the first user message: add a line "Budget: M model calls, S seconds." after the work directory line.
4. Tests in primitives/apprentice/tests with the FakeModel: the two step notes (once each, at the right steps), the time note (use the clock that Apprentice takes), the last call at the step limit (summary text from the model, status step_limit), a failed last call, and no notes in a short task.
5. Update primitives/apprentice/README.md (ASD-STE100) where it explains the limits.

Keep the change small. Do not change the tools or the model client.
Verify: cd primitives/apprentice && python3 -m unittest discover -s tests -q. Then ./tools/check.sh.
