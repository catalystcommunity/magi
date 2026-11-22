"""The tester: tests from the task text, run on the change before a review.

A review reads the change. On the coding eval, the review of the worker
model, the review of a second model, and an audit of each requirement all
passed changes that did not do a sentence of the task (a limit of 20000
characters cut at 8000, a failed import that returned no error). A test
that runs finds such a defect each time. The worker writes tests too, but
it writes them from its code, so they agree with the code.

The tester is a second run of the worker model with an empty context. It
gets the sentences of the task with a number each, and the work directory
with the change. It writes one test file and runs it. The harness does
the parts that a model does not do reliably:

- Each sentence has a test or a line that says why it has none. The first
  tester had no such rule and left out the sentences that the change got
  wrong.
- A Python test file of the tester has no fake of its own. The first
  tester wrote a fake of the Tools object in the form of the code, and all
  its tests passed on a change that stopped with a TypeError on the real
  object.
- The harness runs the test file itself: the report of the model is not
  the source.
- A finding is a test that stops with an exception in a file of the
  change. On six fixed changes, the tests that failed on an assertion were
  wrong each time (a wrong setup, an expectation that the task does not
  give), also on two changes that a person approved. The tests that
  stopped in the changed code were right each time. The transcript keeps
  the other failed tests.

The harness then puts the work directory back as it was: the tester cannot
change the work. Each failed test goes to the worker with its sentence and
its source, as an item of the review round. The tester can be wrong, and
the worker can say so in its summary.
"""
from __future__ import annotations

import posixpath
import re
import shlex
from dataclasses import replace
from typing import Any

from .apprentice import DIFF_GATE_REJECTS, RESTORE_SCRIPT, SNAPSHOT_SCRIPT, Apprentice, Task
from .config import Limits
from .diffgate import TEST_FILE
from .model import Model
from .testgate import bodies
from .tools import Tools, as_text
from .transcript import Transcript

MAX_STEPS = 40
MAX_SECONDS = 1200
MAX_ITEMS = 4
MAX_BODY_CHARS = 1500
MAX_SENTENCES = 40
RUN_TIMEOUT_S = 600
PY_FILE, GO_FILE = "test_tester_probe.py", "tester_probe_test.go"

INSTRUCTIONS = """You are the tester of a change. A different engineer did the task below. The change is in the work directory and is not committed: `git status --short` shows the files that it touched.

Your work: find the sentences of the task that the change does not do, with tests that you run. A change that passes its own tests can still be wrong: the engineer wrote those tests from the code.

The sentences of the task have numbers (S1, S2, ...). Do the steps in this order.

Step 1. Find how to drive the real code. Read the tests that were there before the change (`git show HEAD:<path>` shows a file before the change). Find the highest entry point that those tests use for this area: the run of the whole object, the handler, the command.

Step 2. Create ONE new test file next to those tests: {py} for Python, {go} for Go. For each sentence, write one of these:
- a test that starts with a comment or a docstring with the number and the expected result in the words of the task, for example "S4: a file of 25000 characters is cut at 20000";
- a comment line "# S9: no test: <cause>" (in Go "// S9: no test: <cause>"), for a sentence about documentation, about the tests, or with no behavior.
A sentence with two behaviors gets two tests. Test the value that the sentence gives, and the cases that it names: the limit value itself, a failed call, the second time.

Rules for the tests:
- Go through the entry point of step 1 with real objects: real files in a temporary directory, a real git repository, the real classes. Use the helpers that the tests had before the change.
- Do not write a fake, a stub, or a mock in your file. Do not use a helper that the change added or edited.
- Do not call the new helper functions of the change directly: their names are not in the task.
- The expected value comes from the sentence. Do not make it weaker to agree with the code.

Step 3. Run your tests. For each test that fails, find the cause.
- The test is wrong (a wrong call, a wrong setup): correct the call or the setup. Do not change the expected value.
- The code does not do what the sentence says, or it stops with an exception on real objects: keep the test as it is. It is a finding.
When you finish, each test that fails must be a finding. The harness runs your file itself.

Do not change a file other than your test file. Do not correct the code.

Then call finish with a short summary: the findings, one line each.

The sentences of the task:
{sentences}

The full task:
<<<
{task}
>>>
"""

# The check command of the tester run: the rules on its test file. $1.. are
# not used; N is the count of sentences.
GATE = r"""f=$(git ls-files --others --exclude-standard | grep -E '(^|/)(test_tester_probe\.py|tester_probe_test\.go)$' | head -n 1)
if [ -z "$f" ]; then echo "There is no new test file test_tester_probe.py or tester_probe_test.go."; exit 1; fi
case "$f" in *.py)
  bad=$(grep -nE '^\s*class [A-Za-z_]*(Fake|Mock|Stub)|unittest\.mock|^\s*import mock|MagicMock' "$f")
  if [ -n "$bad" ]; then echo "Your test file has a fake, a stub, or a mock of its own. Remove it. Use real objects and the helpers that the tests had before the change."; echo "$bad"; exit 1; fi;;
esac
missing=""
for i in $(seq 1 {n}); do grep -qE "S$i([^0-9]|$)" "$f" || missing="$missing S$i"; done
if [ -n "$missing" ]; then echo "These sentences have no test and no 'no test' line in $f:$missing"; exit 1; fi
echo "the test file follows the rules"
"""

FIND_FILE = ("git ls-files --others --exclude-standard | "
             r"grep -E '(^|/)(test_tester_probe\.py|tester_probe_test\.go)$' | head -n 1")
PY_RESULT = re.compile(r"^(test_\w+) \([\w.]+\)(?:\n.*?)? \.\.\. (ok|FAIL|ERROR)$", re.M)
PY_BLOCK = re.compile(r"^(?:FAIL|ERROR): (test_\w+) .*?\n-{20,}\n(.*?)(?=\n={20,}\n|\n-{20,}\nRan )", re.M | re.S)
GO_FAIL = re.compile(r"^\s*--- FAIL: (Test\w+)", re.M)
GO_TEST = re.compile(r"^func (Test\w+)\(", re.M)
GO_PANIC = re.compile(r"^panic: ", re.M)
PY_FRAME = re.compile(r'^\s+File "([^"]+)", line \d+', re.M)
CHANGED_FILES = "git diff --name-only HEAD; git ls-files --others --exclude-standard"
SENTENCE_REF = re.compile(r"\bS(\d+)\b")
_CHECKS = re.compile(r"^\s*#*\s*acceptance checks:?\s*$", re.I | re.M)
_SPLIT = re.compile(r"(?<=[.!?:;])\s+(?=[A-Z\"`(])")
_MARKER = re.compile(r"^\s*(?:[-*]|\d+[.)])\s+")


class _Prefixed:
    """The records of the tester in the transcript of the task, with their
    own kinds: a reader of the transcript must not take the result of the
    tester for the result of the task."""

    def __init__(self, transcript: Transcript):
        self._tr = transcript
        self.redactor = transcript.redactor

    def record(self, kind: str, **data: Any) -> None:
        self._tr.record("tester_" + kind, **data)


def sentences(task_text: str) -> list[str]:
    """The sentences of the task that can ask for a behavior. The acceptance
    checks of the spec step are not in: they are a reading of the task by a
    model, and they named fakes as the place to test."""
    m = _CHECKS.search(task_text)
    text = task_text[:m.start()] if m else task_text
    out: list[str] = []
    for line in text.splitlines():
        line = _MARKER.sub("", line.strip())
        if not line or line.startswith("#"):
            continue
        for s in _SPLIT.split(line):
            s = s.strip()
            if len(s.split()) >= 4 and s not in out:
                out.append(s)
    return out[:MAX_SENTENCES]


def failed_tests(path: str, output: str) -> dict[str, str]:
    """The tests of the test file that failed in the run, each with the
    text of its failure."""
    if path.endswith(".go"):
        out = {}
        for name in GO_FAIL.findall(output):
            # With -v, the lines of a test come before its result line.
            out[name] = output.split(f"--- FAIL: {name}", 1)[0].rsplit(f"=== RUN   {name}\n", 1)[-1].strip()
        if m := GO_PANIC.search(output):
            # A panic stops the test binary: the test that ran has no result line.
            running = re.findall(r"^=== RUN   (Test\w+)$", output[:m.start()], re.M)
            out[running[-1] if running else "TestMain"] = output[m.start():].strip()
        return out
    blocks = {name: body.strip() for name, body in PY_BLOCK.findall(output)}
    return {name: blocks.get(name, "") for name, result in PY_RESULT.findall(output) if result != "ok"}


def crashed(failure: str, changed: list[str]) -> bool:
    """True when the failure is an exception that came through a file of
    the change, and not an assertion or an error of the test itself."""
    code = [f for f in changed if not TEST_FILE.search(f)]
    if "panic:" in failure:
        frames = re.findall(r"^\s+(\S+\.go):\d+", failure, re.M)
        return any(fr.endswith("/" + f) or fr == f for fr in frames for f in code)
    frames = PY_FRAME.findall(failure)
    if not frames or TEST_FILE.search(frames[-1]) or re.search(r"^AssertionError\b", failure, re.M):
        return False
    return any(fr.endswith("/" + f) or fr == f for fr in frames for f in code)


def short(failure: str) -> str:
    return " ".join(failure.splitlines()[-2:])[:400]


def blockers(path: str, source: str, crashes: dict[str, str], task_sentences: list[str]) -> list[dict]:
    """The tests that stopped in the changed code, as items of the review round."""
    by_name = bodies(source, go=path.endswith(".go"))
    out = []
    for name, why in crashes.items():
        body = by_name.get(name, "")
        refs = [int(n) for n in SENTENCE_REF.findall(body[:400])]
        said = [task_sentences[n - 1] for n in refs if 0 < n <= len(task_sentences)][:2]
        problem = ("A test that the tester wrote from the task text stops with an exception in the changed code."
                   + (" The task says: " + " ".join(f'"{s}"' for s in said) if said else "")
                   + (f" The failure: {short(why)}" if why else "")
                   + f"\nThe test ({name}; it is not in the work directory):\n{body[:MAX_BODY_CHARS]}")
        out.append({"lens": "tester", "file": path, "line": None, "problem": problem})
    return out[:MAX_ITEMS]


def run_command(path: str, source: str) -> str:
    """The command that runs only the test file of the tester."""
    d, name = posixpath.split(path)
    if path.endswith(".go"):
        names = "|".join(GO_TEST.findall(source)) or "^$"
        return f"cd {shlex.quote(d or '.')} && go test -count=1 -v -run {shlex.quote('^(' + names + ')$')} . 2>&1"
    top, tests = posixpath.split(d)
    return (f"cd {shlex.quote(top or '.')} && python3 -m unittest discover -s {shlex.quote(tests or '.')} "
            f"-p {shlex.quote(name)} -v 2>&1")


def run(model: Model, tools: Tools, task_text: str, limits: Limits, transcript: Transcript) -> list[dict]:
    """Run the tester on the work directory. Returns its findings as review
    items. The work directory is the same after it."""
    snap = tools.ws.sh(SNAPSHOT_SCRIPT, timeout_s=60)
    lines = as_text(snap.stdout).strip().splitlines()[-1:] if snap.exit_code == 0 else []
    if not lines or not re.fullmatch(r"[0-9a-f]{40,64}", lines[0]):
        transcript.record("tester", error="no snapshot of the work directory")
        return []
    tree = lines[0]
    said = sentences(task_text)
    sub = replace(limits, max_steps=min(MAX_STEPS, limits.max_steps), max_seconds=min(MAX_SECONDS, limits.max_seconds),
                  self_review=False, test_gate=False)
    instructions = INSTRUCTIONS.format(py=PY_FILE, go=GO_FILE, task=task_text,
                                       sentences="\n".join(f"S{i}. {s}" for i, s in enumerate(said, 1)))
    path, source, failed, changed, result = "", "", {}, [], None
    try:
        run_ = Apprentice(model, tools, sub, _Prefixed(transcript))
        # The diff rules are for the worker: the diff here is its change.
        run_._gate_rejects = DIFF_GATE_REJECTS
        result = run_.run(Task(instructions, workdir=tools.ws.workdir, check_command=GATE.replace("{n}", str(len(said)))))
        path = as_text(tools.ws.sh(FIND_FILE, timeout_s=60).stdout).strip()
        if path:
            source = as_text(tools.ws.sh("cat " + shlex.quote(path), timeout_s=60).stdout)
            # The harness runs the file: a test fails or it does not, and
            # the report of the model cannot change that.
            r = tools.ws.sh(run_command(path, source), timeout_s=RUN_TIMEOUT_S)
            failed = failed_tests(path, as_text(r.stdout))
            changed = [f for f in as_text(tools.ws.sh(CHANGED_FILES, timeout_s=60).stdout).split() if f != path]
    finally:
        back = tools.ws.sh(RESTORE_SCRIPT.format(tree=tree), timeout_s=120)
    crashes = {name: why for name, why in failed.items() if crashed(why, changed)}
    found = blockers(path, source, crashes, said)
    transcript.record("tester", status=result.status if result else "error", steps=result.steps if result else 0,
                      file=path, sentences=len(said), failed={n: short(w) for n, w in sorted(failed.items())},
                      crashed=sorted(crashes),
                      findings=[b["problem"] for b in found], restored=back.exit_code == 0)
    return found
