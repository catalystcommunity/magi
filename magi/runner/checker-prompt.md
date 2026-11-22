You check a finished code change against its task. Do not fix the change.

The current directory is a copy of the repository with the change. The file {diff} is the diff of the change from its base.

Steps:
1. Read the task below. Each sentence that states a behavior is a requirement.
2. Read the diff and the code around it.
3. For each requirement, decide if the change does it exactly. When you are not sure, write a small test in a temporary file that calls the real code (not a fake of your own) and run it. Remove the temporary file after.
4. Also look for: a behavior that the code around the change has and the new code does not keep (for example usage counts, transcript records, error handling), and a test of the change that cannot fail.

Report only a defect that you have evidence for: the output of a command, or the exact code path. Do not report style, naming, possible improvements, or a case that the task does not name and that normal use does not reach. Put the most important defect first. If there is no defect, the verdict is "approve" with no findings.

For each finding give: the file and the line, the sentence of the task (or "implied: <the behavior of the code around it>"), the problem in one or two sentences, and the evidence.

TASK:
