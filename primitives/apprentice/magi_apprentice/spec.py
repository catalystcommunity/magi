"""The spec step: acceptance checks from the task instructions, before the work.

A model reads only the instructions and writes one check for each sentence
that asks for a behavior: the quote, a result that a test can compare, and
the entry point where a test sees it. The checks go at the end of the
instructions, so the worker, the diff gate, the self-review, and the work
review all read them. In the coding eval (evals/coding, pass 4) the checks
cut the task sentences that the change did differently from 9 to 3.
"""
from __future__ import annotations

import re
import time

from .model import Model

HEADING = "Acceptance checks:"

PROMPT = """You prepare a coding task for an agent that works alone. The agent reads the task, changes the repository, and a reviewer compares the change with the task. Your text goes at the end of the task, under the heading "Acceptance checks". You add no new requirements: each check comes from a sentence of the task.

Steps:

1. Find each sentence of the task that asks for a behavior: what the code does, what it returns, what it writes, what happens in a case ("if", "when", "unless", "at most", "once", "after", "before", "through", "the same").
2. For each sentence, write one check:
   - Quote the sentence (or the part of it with the behavior) in double quotes.
   - Give the observable result: a concrete input or state, and the exact output, record, or text that a test can compare. Use the numbers and names of the task.
   - Give the seam: the entry point that runs the whole flow, where a test can see the result (for example the function that starts the loop, or the step that runs the command). Use only names that the task gives. Do not name a helper, and do not invent a function name: a check must not make the agent split the code into new functions. When the task names no entry point, write "the entry point that runs this flow". Name the test fakes of the task when it names them.
3. For each word of place, count, or order (for example "after the work directory line", "at most 50", "once", "one exec"), write the check so that a wrong place, count, or order fails it.
4. For each "if X fails" or "when X is missing" sentence, give the input that makes X fail and the result that the task asks for in that case.

You are done when every behavior sentence of the task has one check, and each check has a quote, a result, and a seam. Write only the checks, as a numbered list, with no other text.

Example of one check:
3. "If the cache file is missing, start with an empty cache and log one warning." Result: a work directory with no cache file; load_state returns a state with 0 entries, and the log has exactly one line that contains "cache missing". Seam: Service.start with the fake store of the tests, not the private loader.
"""

# A model can repeat the heading of the prompt; the caller adds its own.
_LEADING_HEADING = re.compile(r"\A\s*#*\s*acceptance checks:?\s*\n", re.I)


def has_checks(instructions: str) -> bool:
    return re.search(r"^\s*#*\s*acceptance checks:?\s*$", instructions, re.I | re.M) is not None


def acceptance_checks(model: Model, instructions: str) -> tuple[str, dict]:
    """The checks for the instructions ("" when the model gives none), and a
    record for the transcript. A failed call gives no checks: the task runs
    as it was written."""
    t0 = time.monotonic()
    rec: dict = {}
    try:
        reply = model.complete([{"role": "system", "content": PROMPT},
                                {"role": "user", "content": "Task:\n\n" + instructions.strip()}], [])
        text = _LEADING_HEADING.sub("", (reply.content or "").strip()).strip()
    except Exception as e:  # the spec step only helps; it must not stop the task
        text, rec["error"] = "", f"{type(e).__name__}: {e}"
    rec.update(chars=len(text), seconds=round(time.monotonic() - t0, 1))
    return text, rec


def with_checks(instructions: str, checks: str) -> str:
    return f"{instructions.rstrip()}\n\n{HEADING}\n\n{checks}" if checks else instructions
