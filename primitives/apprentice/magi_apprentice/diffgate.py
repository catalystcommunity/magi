"""Rules on the diff that the harness checks itself when the model calls
finish, before the check command. No model decides these.

Each rule comes from a correction that a reviewer had to make more than
once, and that a fixed rule finds every time:
- An executable file lost its executable bit. edit_file and write_file
  write files with mode 644, so an edit of a script makes it 644.
- A test file was deleted, and the task does not say to remove tests.
- The same block of new code is in two places of one file, instead of in
  one helper or constant (evals/coding pr3 and pr5: a note text copied
  three times). On the last 300 commits of main, the rule found 5 commits
  of approved code, so the model can keep copies when it says why.
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
    "echo '--- diff ---'",
    "git diff -U0 2>/dev/null",
    "git reset -q",
])

MODE_LOST = re.compile(r"^\s*mode change 100755 => 100644 (\S+)", re.M)
TEST_FILE = re.compile(r"(^|/)(test_[^/]+\.py|[^/]+_test\.(go|py)|[^/]+\.test\.[jt]sx?|tests?/[^/]+)$")
REMOVE_TESTS = re.compile(r"\b(remove|delete|drop)\b[^.\n]*\btests?\b", re.I)
# Copies in tests are often clearer than a helper, and generated files are
# copies by design.
CODE_FILE = re.compile(r"\.(go|py|tsx?|rs|sh)$")
NOT_CHECKED = re.compile(r"(\.gen\.go$|\.gen\.ts$|\.d\.ts$|/api/|^clients/|/vendor/|static/js/)")
# Lines that are the same in many correct functions.
BOILERPLATE = re.compile(r"^(if err != nil \{|return .*\berr\)?|return( nil)?|\}.*|\)|else:|try:|pass|return None)$")
# The copy rule is a hint: the gate gives it once in a run, and a finish
# that keeps the copies with a reason goes on.
COPIES = "The change adds the same block of code in two or more places: "
DUP_LINES = 4      # a copied block has at least this many lines
DUP_MIN_CHARS = 12  # shorter lines (a brace, "return x") are not counted


def _sections(out: str) -> tuple[str, str, str]:
    summary, _, rest = out.partition("--- name-status ---")
    name_status, _, diff = rest.partition("--- diff ---")
    return summary.replace("--- summary ---", ""), name_status, diff


def duplicated_blocks(diff: str) -> list[tuple[str, str]]:
    """(file, first line) of the first block of DUP_LINES added lines that
    is also added in another place of the same file; one for each file."""
    added: dict[str, list[str]] = {}
    cur = None
    for line in diff.splitlines():
        if line.startswith("+++ "):
            cur = line[6:] if line.startswith("+++ b/") else None
            if cur and (not CODE_FILE.search(cur) or NOT_CHECKED.search(cur) or TEST_FILE.search(cur)):
                cur = None
        elif cur and line.startswith("+"):
            text = line[1:].strip()
            if len(text) >= DUP_MIN_CHARS and not text.startswith(("#", "//")) and not BOILERPLATE.match(text):
                added.setdefault(cur, []).append(text)
    found = []
    for path, lines in added.items():
        first: dict[tuple, int] = {}
        for i in range(len(lines) - DUP_LINES + 1):
            block = tuple(lines[i:i + DUP_LINES])
            if block not in first:
                first[block] = i
            elif i - first[block] >= DUP_LINES:
                found.append((path, block[0]))
                break
    return found


def problems(out: str, task_text: str) -> list[str]:
    """The rule breaks in the output of GATE_SCRIPT, as instructions."""
    summary, name_status, diff = _sections(out)
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
    dups = duplicated_blocks(diff)
    if dups:
        found.append(COPIES + "; ".join(f"{p} (it starts with: {first[:80]})" for p, first in dups[:5])
                     + ". Put the shared code in one helper or constant, or say in the summary why the copies must stay.")
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
