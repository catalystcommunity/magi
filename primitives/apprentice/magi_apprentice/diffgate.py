"""Rules on the diff that the harness checks itself when the model calls
finish, before the check command. No model decides these.

Each rule comes from a correction that a reviewer had to make more than
once, and that a fixed rule finds every time:
- An executable file lost its executable bit. edit_file and write_file
  write files with mode 644, so an edit of a script makes it 644.
- A test file was deleted, and the task does not say to remove tests.
"""
from __future__ import annotations

import re

from .tools import Tools, as_text

# New files go in the index as empty entries, so that the summary shows
# them; the index is reset at the end (see DIFF_SCRIPT in apprentice.py).
GATE_SCRIPT = "\n".join([
    "git add --intent-to-add --all 2>/dev/null",
    "echo '--- summary ---'",
    "git diff --summary 2>/dev/null",
    "echo '--- name-status ---'",
    "git diff --name-status 2>/dev/null",
    "git reset -q",
])

MODE_LOST = re.compile(r"^\s*mode change 100755 => 100644 (\S+)", re.M)
TEST_FILE = re.compile(r"(^|/)(test_[^/]+\.py|[^/]+_test\.(go|py)|[^/]+\.test\.[jt]sx?|tests?/[^/]+)$")
REMOVE_TESTS = re.compile(r"\b(remove|delete|drop)\b[^.\n]*\btests?\b", re.I)


def _sections(out: str) -> tuple[str, str]:
    summary, _, rest = out.partition("--- name-status ---")
    return summary.replace("--- summary ---", ""), rest


def problems(out: str, task_text: str) -> list[str]:
    """The rule breaks in the output of GATE_SCRIPT, as instructions."""
    summary, name_status = _sections(out)
    found = []
    lost = MODE_LOST.findall(summary)
    if lost:
        found.append("These files were executable and are not now: " + ", ".join(lost)
                     + ". Run chmod 755 on each, and check with git diff --summary (no mode change).")
    deleted = [line.split("\t", 1)[1] for line in name_status.splitlines()
               if line.startswith("D\t") and TEST_FILE.search(line.split("\t", 1)[1])]
    if deleted and not REMOVE_TESTS.search(task_text or ""):
        found.append("The change deletes test files and the task does not ask for it: " + ", ".join(deleted)
                     + ". Restore them with git checkout -- <file>, or say in the summary why they must go.")
    return found


def check(tools: Tools, task_text: str) -> list[str]:
    """Run the rules on the work directory. A failed command finds nothing:
    the gate must not block a finish because of its own problem."""
    try:
        r = tools.ws.sh(GATE_SCRIPT, timeout_s=60)
    except Exception:
        return []
    if r.exit_code != 0:
        return []
    return problems(as_text(r.stdout), task_text)
