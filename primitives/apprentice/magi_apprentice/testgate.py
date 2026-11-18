"""Test the tests: the new tests of a change, run on the code without it.

A new test that passes on the code before the change does not test the
change. The coding eval found such tests in reviews that a person had to
send back: a test that copies the logic of the code into the test and
checks the copy (pr5), and a test whose input never reaches the changed
path (pr7). A reviewer model did not find them; a run finds them every
time. On the 38 cases of evals/review, it also finds a test in 2 of the
22 changes that a person approved: a test that checks that something does
not happen passes without the change too. Thus the note is a request that
the model can answer in its summary, and the harness gives it once.

The harness makes a git worktree of HEAD (the code before the change),
copies the changed test files into it, runs only the new tests, and
removes the worktree. The work directory does not change. A test that does
not compile or fails there is good: it needs the change.
"""
from __future__ import annotations

import os
import re
import shlex

from .tools import Tools, as_text

GO_TEST = re.compile(r"^func (Test\w+)\(t \*testing\.T\)")
PY_TEST = re.compile(r"^\s+def (test_\w+)\(self")
FILE = re.compile(r"^\+\+\+ b/(\S+)$", re.M)
PY_TEST_FILE = re.compile(r"(^|/)test_[^/]+\.py$")
PY_PASS = re.compile(r"^(test_\w+) \(.*\) \.\.\. ok$", re.M)
GO_PASS = re.compile(r"^\s*--- PASS: (Test\w+)", re.M)
MAX_TESTS = 30
TIMEOUT_S = 600

# The new test files are in the diff only after this (see DIFF_SCRIPT).
DIFF = "git add --intent-to-add --all 2>/dev/null; git diff 2>/dev/null; git reset -q"


def new_tests(diff: str) -> dict[str, list[str]]:
    """The test functions that the diff adds, by test file."""
    out: dict[str, list[str]] = {}
    for part in re.split(r"^diff --git ", diff, flags=re.M)[1:]:
        m = FILE.search(part)
        if not m:
            continue
        path = m.group(1)
        pat = GO_TEST if path.endswith("_test.go") else PY_TEST if PY_TEST_FILE.search(path) else None
        if pat is None:
            continue
        names = [t.group(1) for line in part.splitlines()
                 if line.startswith("+") and (t := pat.match(line[1:]))]
        if names:
            out[path] = names
    return out


def bodies(text: str, go: bool) -> dict[str, str]:
    """The body of each test function in a file, by name."""
    pat, out, lines = (GO_TEST if go else PY_TEST), {}, text.splitlines()
    for i, line in enumerate(lines):
        if not (m := pat.match(line)):
            continue
        body = []
        indent = len(line) - len(line.lstrip())
        for ln in lines[i + 1:]:
            if go and ln.startswith("}"):
                break
            if not go and ln.strip() and len(ln) - len(ln.lstrip()) <= indent:
                break
            body.append(ln)
        out[m.group(1)] = "\n".join(body).strip()
    return out


def drop_renames(tests: dict[str, list[str]], old: dict[str, str], new: dict[str, str]) -> dict[str, list[str]]:
    """Without the new tests that have the body of a test of HEAD: a rename
    passes without the change, as it must. old and new hold the text of
    each test file at HEAD and in the work directory."""
    out = {}
    for path, names in tests.items():
        go = path.endswith(".go")
        before = set(bodies(old.get(path, ""), go).values())
        after = bodies(new.get(path, ""), go)
        kept = [n for n in names if after.get(n) not in before]
        if kept:
            out[path] = kept
    return out


def _run_commands(tests: dict[str, list[str]], files: set[str]) -> list[str]:
    """One shell command for each test file, run in the worktree."""
    cmds, n = [], 0
    for path, names in sorted(tests.items()):
        names = names[:max(0, MAX_TESTS - n)]
        n += len(names)
        if not names:
            break
        d, base = os.path.dirname(path) or ".", os.path.basename(path)
        if path.endswith(".go"):
            mod = d
            while mod not in ("", ".") and f"{mod}/go.mod" not in files:
                mod = os.path.dirname(mod)
            mod = mod or "."
            rel = "./" + os.path.relpath(d, mod) if d != mod else "."
            run = "^(" + "|".join(names) + ")$"
            cmds.append(f"(cd {shlex.quote(mod)} && GOWORK=off go test -count=1 -v -run {shlex.quote(run)} "
                        f"{shlex.quote(rel)} 2>&1)")
        else:
            # The tests of this repository import their helpers (fakes.py)
            # from the test directory, so discover runs in its parent.
            root, start = (os.path.dirname(d) or ".", os.path.basename(d)) if os.path.basename(d) == "tests" \
                else (d, ".")
            ks = " ".join(f"-k {shlex.quote(x)}" for x in names)
            cmds.append(f"(cd {shlex.quote(root)} && python3 -m unittest discover -s {shlex.quote(start)} "
                        f"-p {shlex.quote(base)} {ks} -v 2>&1)")
    return cmds


def script(tests: dict[str, list[str]], files: set[str]) -> str:
    """The shell script that runs the new tests on HEAD in a worktree."""
    copy = " ".join(shlex.quote(p) for p in sorted(tests))
    return "\n".join([
        "set -u",
        'wt=$(mktemp -d)',
        'git worktree add -q --detach "$wt" HEAD >/dev/null 2>&1 || { echo "no worktree"; exit 0; }',
        f'for f in {copy}; do mkdir -p "$wt/$(dirname "$f")"; cp "$f" "$wt/$f"; done',
        'cd "$wt"',
        *_run_commands(tests, files),
        'cd - >/dev/null',
        'git worktree remove --force "$wt" >/dev/null 2>&1; rm -rf "$wt"',
    ])


def passing(out: str) -> list[str]:
    """The tests that passed in the output of script."""
    return sorted(set(PY_PASS.findall(out)) | set(GO_PASS.findall(out)))


def check(tools: Tools) -> list[str]:
    """The new tests of the work directory that pass without the change.
    A failed command finds nothing: the gate must not block a finish
    because of its own problem."""
    try:
        diff = as_text(tools.ws.sh(DIFF, timeout_s=60).stdout)
        tests = new_tests(diff)
        old = {p: as_text(tools.ws.sh(f"git show HEAD:{shlex.quote(p)} 2>/dev/null", timeout_s=60).stdout)
               for p in tests}
        new = {p: as_text(tools.ws.sh(f"cat -- {shlex.quote(p)}", timeout_s=60).stdout) for p in tests}
        tests = drop_renames(tests, old, new)
        if not tests:
            return []
        files = set(as_text(tools.ws.sh("git ls-files", timeout_s=60).stdout).split())
        r = tools.ws.sh(script(tests, files), timeout_s=TIMEOUT_S)
    except Exception:
        return []
    return passing(as_text(r.stdout))


def note(names: list[str]) -> str:
    return ("finish rejected: these new tests pass on the code without your change, so they do not "
            "test it: " + ", ".join(names[:10]) + ". A test must call the changed code and fail without "
            "the change. Change each test so that it does. A test that checks that something does not "
            "happen, or that tests behavior that was there before, can stay: then say so in the finish "
            "summary. Then call finish again.")
