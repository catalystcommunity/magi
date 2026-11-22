"""The tester: tests from the task text, run on the change before a review.

A review reads the change. On the coding eval, the review of the worker
model, the review of a second model, and an audit of each requirement all
passed changes that did not do a sentence of the task (a limit of 20000
characters cut at 8000, a failed import that returned no error). A test
that runs finds such a defect each time. The worker writes tests too, but
it writes them from its code, so they agree with the code.

The tester is a second run of the worker model with an empty context. It
gets the task and the work directory with the change, writes one test file
from the words of the task, and runs it. It can run its tests, so it can
correct a wrong test setup. It reports the tests that fail because the code
does not do what the task says. The harness then puts the work directory
back as it was: the tester cannot change the work. Each failed test goes to
the worker with its source, as an item of the review round. The tester can
be wrong, and the worker can say so in its summary.
"""
from __future__ import annotations

import json
import re
import shlex
from dataclasses import replace
from typing import Any

from .apprentice import DIFF_GATE_REJECTS, RESTORE_SCRIPT, SNAPSHOT_SCRIPT, Apprentice, Task
from .config import Limits
from .model import Model
from .testgate import bodies
from .tools import Tools, as_text
from .transcript import Transcript

MAX_STEPS = 40
MAX_SECONDS = 1200
MAX_ITEMS = 4
MAX_BODY_CHARS = 1500

INSTRUCTIONS = """You are the tester of a change. A different engineer did the task below. The change is in the work directory and is not committed: `git status` and `git diff` show it.

Your work: find the parts of the task that the change does not do, with tests that you run.

Rules:
- Write ONE new test file next to the tests of the code that changed. Name it test_tester_probe.py for Python or tester_probe_test.go for Go. Do not change a different file. Do not correct the code.
- Go through the task sentence by sentence. Each behavior, exact text, number, limit, order, and "if ... fails" case gets one small test. Give each test a name that tells the sentence.
- Take the expected result from the words of the task, not from the code. If the task says 20000, the test expects 20000. Do not make an expectation weaker because the code does a different thing.
- Call the real code, in the way that the tests around your file call it. Read those tests first and use their helpers and fakes.
- Do not copy the tests that the change added. Test the sentences that they do not test, and the edges: the limit value itself, a failure of a call, the second time.
- Run your tests. When a test fails, find the cause. If the test is wrong (a wrong helper, a wrong setup, an expectation that the task does not give), correct the test. If the code does not do what the task says, keep the test as it is: this is a finding.
- A sentence about documentation or about the tests gets no test.

Then call finish. The summary is one JSON object and nothing else:
{"file": "<path of your test file>", "passed": <count of tests that pass>, "failing": [{"test": "<test name>", "requirement": "<the sentence of the task>", "why": "<what the task says and what the code does>"}]}
Put in "failing" only the findings: the tests that fail because of the code.

The task:
<<<
%s
>>>
"""


class _Prefixed:
    """The records of the tester in the transcript of the task, with their
    own kinds: a reader of the transcript must not take the result of the
    tester for the result of the task."""

    def __init__(self, transcript: Transcript):
        self._tr = transcript
        self.redactor = transcript.redactor

    def record(self, kind: str, **data: Any) -> None:
        self._tr.record("tester_" + kind, **data)


def parse_summary(text: str) -> dict:
    """The JSON object of the finish summary, or {} when there is none."""
    start, end = text.find("{"), text.rfind("}")
    if start < 0 or end <= start:
        return {}
    try:
        d = json.loads(text[start:end + 1])
    except ValueError:
        return {}
    return d if isinstance(d, dict) else {}


def blockers(report: dict, source: str) -> list[dict]:
    """The findings of the tester as items of the review round."""
    path = str(report.get("file") or "")
    by_name = bodies(source, go=path.endswith(".go"))
    out = []
    for f in report.get("failing") or []:
        if not isinstance(f, dict) or not f.get("test"):
            continue
        name = re.sub(r"\W.*", "", str(f["test"]).split(".")[-1])
        body = by_name.get(name, "")
        if not body:
            # A finding with no test that the harness can show is a claim
            # only: the worker has nothing to run.
            continue
        problem = (f"A test that the tester wrote from the task text fails. The task says: "
                   f"\"{str(f.get('requirement') or '').strip()}\". {str(f.get('why') or '').strip()}\n"
                   f"The test ({name}; it is not in the work directory):\n{body[:MAX_BODY_CHARS]}")
        out.append({"lens": "tester", "file": path, "line": None, "problem": problem})
    return out[:MAX_ITEMS]


def run(model: Model, tools: Tools, task_text: str, limits: Limits, transcript: Transcript) -> list[dict]:
    """Run the tester on the work directory. Returns its findings as review
    items. The work directory is the same after it."""
    snap = tools.ws.sh(SNAPSHOT_SCRIPT, timeout_s=60)
    lines = as_text(snap.stdout).strip().splitlines()[-1:] if snap.exit_code == 0 else []
    if not lines or not re.fullmatch(r"[0-9a-f]{40,64}", lines[0]):
        transcript.record("tester", error="no snapshot of the work directory")
        return []
    tree = lines[0]
    sub = replace(limits, max_steps=min(MAX_STEPS, limits.max_steps), max_seconds=min(MAX_SECONDS, limits.max_seconds),
                  self_review=False, test_gate=False)
    report, source, result = {}, "", None
    try:
        run_ = Apprentice(model, tools, sub, _Prefixed(transcript))
        # The diff rules are for the worker: the diff here is its change.
        run_._gate_rejects = DIFF_GATE_REJECTS
        result = run_.run(Task(INSTRUCTIONS % task_text, workdir=tools.ws.workdir))
        report = parse_summary(result.summary or "") if result.status == "finished" else {}
        path = str(report.get("file") or "")
        if path and ".." not in path and not path.startswith("/"):
            r = tools.ws.sh("cat " + shlex.quote(path), timeout_s=60)
            source = as_text(r.stdout) if r.exit_code == 0 else ""
    finally:
        back = tools.ws.sh(RESTORE_SCRIPT.format(tree=tree), timeout_s=120)
    found = blockers(report, source)
    transcript.record("tester", status=result.status if result else "error", steps=result.steps if result else 0,
                      passed=report.get("passed"), claimed=len(report.get("failing") or []),
                      findings=[b["problem"] for b in found], restored=back.exit_code == 0)
    return found
