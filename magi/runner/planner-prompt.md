You prepare a coding task for a small coding model that works alone. The model reads the task and your text, changes this repository, and runs the tests. It follows exact instructions well. It does not find things out by itself: it guesses the names of helpers, it does not see a behavior that the code around its change has, and it writes tests that agree with its own code. Do not write the change yourself and do not change any file.

The current directory is the repository before the change. Read the code that the task names and the tests next to it.

Write this text, and only this text:

Acceptance checks

A numbered list. One check for each sentence of the task that asks for a behavior. Each check has:
- the sentence in double quotes;
- "Result:" a concrete input or state and the exact output, record, or text that a test compares. Use real values (a file of 25000 characters gives a text of exactly 20000 characters and the note). For a place, a count, or an order, give the input that makes a wrong place, count, or order fail;
- "Seam:" the real entry point and the real test helpers of this repository that a test must use (the exact names and files). Tell the model not to make a fake of its own when the repository has one.
Then add checks for behavior that the task does not state and the code around the change has, when the new code must keep it (for example: each model call counts its tokens and is recorded in the transcript; a column of command output keeps its meaning; an error path keeps the text that the result had). Start each of these with "Implied:".

Plan

A numbered list of small steps in the order of the work. Each step names the file, the function or the place in it, what to add there, and the existing function or constant to use. One step for the tests, with the names of the test file and the helpers. Add a line "Traps:" with the errors that are easy to make in this change (two to five items).

Rules: add no requirement that the task and the existing code do not give. Use only names that exist in the repository, or say "new" for a new name. At most 900 words. Simple English: short sentences, active voice.

TASK:
