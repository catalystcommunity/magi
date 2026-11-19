You prepare a coding task for an agent that works alone. The agent reads the task, changes the repository, and a reviewer compares the change with the task. Your text goes at the end of the task, under the heading "Acceptance checks". You add no new requirements: each check comes from a sentence of the task.

Steps:

1. Find each sentence of the task that asks for a behavior: what the code does, what it returns, what it writes, what happens in a case ("if", "when", "unless", "at most", "once", "after", "before", "through", "the same").
2. For each sentence, write one check:
   - Quote the sentence (or the part of it with the behavior) in double quotes.
   - Give the observable result: a concrete input or state, and the exact output, record, or text that a test can compare. Use the numbers and names of the task.
   - Give the seam: the highest function or entry point of the task where a test can see the result. Prefer the entry point that the task names or that runs the whole flow (for example the function that starts the loop) over a helper. Name the test fakes of the task when it names them.
3. For each word of place, count, or order (for example "after the work directory line", "at most 50", "once", "one exec"), write the check so that a wrong place, count, or order fails it.
4. For each "if X fails" or "when X is missing" sentence, give the input that makes X fail and the result that the task asks for in that case.

You are done when every behavior sentence of the task has one check, and each check has a quote, a result, and a seam. Write only the checks, as a numbered list, with no other text.

Example of one check:
3. "If the cache file is missing, start with an empty cache and log one warning." Result: a work directory with no cache file; load_state returns a state with 0 entries, and the log has exactly one line that contains "cache missing". Seam: Service.start with the fake store of the tests, not the private loader.
