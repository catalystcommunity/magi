Acceptance checks

1. "When 80% of max_steps are used, and again when 5 steps are left, add a user message before the next model call: "Budget: N of M model calls are left (and S seconds of the time limit). Finish the work that is necessary, run the check, and call finish with a summary. Do not start new improvements."" Result: Input: max_steps=10, max_seconds=100, current_step=8 triggers first note; current_step=5 triggers second note. Output: Two user messages are injected before the next model call. The first contains "Budget: 2 of 10 model calls are left (and S seconds of the time limit)...", the second contains "Budget: 5 of 10 model calls are left...". Seam: Apprentice.run.

2. "Do the same for max_seconds at 80% (one note)." Result: Input: max_seconds=100, elapsed_time=80. Output: Exactly one user message is injected before the next model call containing "Budget: N of M model calls are left (and 20 seconds of the time limit)...". Seam: Apprentice.run.

3. "Each note goes in once." Result: Input: A run that crosses all thresholds. Output: Exactly one note is recorded at 80% steps, exactly one at 5 steps left, and exactly one at 80% time. Zero duplicates for any threshold. Seam: Apprentice.run.

4. "Record each note in the transcript (kind "budget_note")." Result: Input: A run that triggers step and time notes. Output: The transcript contains entries where `kind` is exactly "budget_note" for each injected note. Seam: Apprentice.run.

5. "When the step limit or the time limit stops the loop and the model did not call finish, make one more model call with no tools: a user message that asks for the finish summary (what changed, how it was verified, what is left undone)." Result: Input: Loop terminates because steps >= max_steps or time >= max_seconds, and `finish` was never called. Output: Exactly one additional model call is executed with an empty tools list, containing a user message asking for the finish summary. Seam: Apprentice.run.

6. "Put that text in Result.summary, and keep the status step_limit or time_limit." Result: Input: The final call succeeds. Output: `Result.summary` is set to the exact text returned by the model, and `Result.status` remains exactly "step_limit" or "time_limit". Seam: Apprentice.run (return value).

7. "If that call fails, keep the current summary text." Result: Input: The final call raises an exception or returns a failure status. Output: `Result.summary` retains the value it held before the final call attempt. Seam: Apprentice.run.

8. "The progress counts in the first user message: add a line "Budget: M model calls, S seconds." after the work directory line." Result: Input: Generation of the first user message. Output: The message string contains the line "Budget: M model calls, S seconds." positioned immediately after the work directory line. Seam: Apprentice.run.

9. "Update primitives/apprentice/README.md (ASD-STE100) where it explains the limits." Result: Input: Read `primitives/apprentice/README.md`. Output: The file contains a section explaining limits written in ASD-STE100 style (simple vocabulary, short sentences, active voice). Seam: File system / README.md.
