"""Run guards: rules in code that stop a run that goes wrong, before it uses
its whole budget.

The guards look only at the tool calls and their results. They do not call a
model, so they cost nothing and give the same answer each time. Each rule
first sends a steering note to the model, then ends the run when the model
does not change its approach. The thresholds come from the runs of
2026-11-15 and 2026-11-16: one grep ran 68 times, one command 94 times, and
one run made its first edit at step 138 of 150.
"""
from __future__ import annotations

import hashlib
import json
import re
from collections import deque
from dataclasses import dataclass

# The same call with the same result: a note at REPEAT_NOTE, the end at
# REPEAT_STOP, counted in the last REPEAT_WINDOW calls.
REPEAT_WINDOW = 10
REPEAT_NOTE = 3
REPEAT_STOP = 5
# The same error from different commands, counted over the whole run.
ERROR_NOTE = 3
ERROR_STOP = 5
# Steps with no change to a file: a note at the limit, the end at
# NO_EDIT_STOP times the limit. A revise round has a shorter limit: the review
# comment says what to change. The end is at 3x, not 2x: a replay of the
# published runs of 2026-11-16 showed a run that verified for 80 steps
# without an edit and then finished well.
NO_EDIT_STEPS = 40
NO_EDIT_STEPS_REVISE = 25
NO_EDIT_STOP = 3
# Thinking cuts over the whole run, not only in one streak of replies. A
# published run had 3 cuts by step 17 and then finished, so the limit is 6.
THINKING_CUTS_STOP = 6

EDIT_TOOLS = {"edit_file", "write_file"}

REPEAT = ("You ran {what} {n} times, and it gave the same result each time. Do not run it "
          "again. Change the approach, or call finish and report what blocks you.")
ERROR = ("The same error came {n} times from different commands:\n{line}\nDo not try more "
         "variants of the same step. Find the cause in the code, or call finish and report "
         "the error and what you tried.")
NO_EDIT = ("You changed no file in {n} steps. Write your plan as a short list of files and "
           "changes, and make the first change now. If you cannot, call finish and say why.")
ENVIRONMENT = ("This error comes from the environment, not from the code: {line}\nDo not try to "
               "fix the environment (no installs, no downloads, no changes outside the "
               "repository). Continue with the work that does not need it, and name the error in "
               "the finish summary.")

# Error lines that the environment causes. A match adds the ENVIRONMENT note
# to the tool result once for each pattern.
ENVIRONMENT_PATTERNS = [
    re.compile(p, re.I) for p in (
        r"connection reset by peer",
        r"connection refused",
        r"could not resolve host|temporary failure in name resolution|no such host",
        r"CONNECT tunnel failed",
        r"unable to update registry|download of \S+ failed",
        r"no space left on device",
        r"virtiofsd did not start",
        r"read-only file system",
    )
]

ERROR_LINE = re.compile(r"error|fail|denied|refused|reset|not found|cannot|unable|panic|fatal", re.I)
# Parts of an error line that change between attempts of the same step.
VOLATILE = re.compile(r"/tmp/\S+|0x[0-9a-f]+|\b[0-9a-f]{12,}\b|\d+(\.\d+)*", re.I)


# The step outcome of each apprentice status. "blocked" stops the task and
# asks a person (later the supervisor): the endpoint or the money is gone, or
# the run went wrong. The work stays in the environment. A guard stop does not
# go to the check, because a check failure would start another work round.
BLOCKED_STATUSES = {"blocked_provider", "model_unavailable", "loop", "no_progress", "stalled",
                    "supervisor_stop", "escalated"}


def outcome_of(status: str) -> str:
    """The workflow outcome of the work step for an apprentice status."""
    if status == "error":
        return "fail"
    if status == "stopped":  # worker shutdown: another worker retries
        return "retry"
    if status in BLOCKED_STATUSES:
        return "blocked"
    return "done"


def round_comment(vars: dict) -> str | None:
    """The comment of a person for the next work round. After a blocked
    round (the work step sets apprentice.blocked), it is the comment of the
    "blocked" decision (resume); otherwise the review comment (revise). The
    runner (magi/runner/steps.go) has the same rule."""
    if vars.get("apprentice.blocked") == "true":
        return vars.get("blocked.comment") or None
    return vars.get("review.comment") or None


@dataclass
class Action:
    """What the loop does after a tool result. note goes to the model as a
    user message; stop, when set, is the status that ends the run."""
    note: str | None = None
    stop: str | None = None
    reason: str = ""


def _digest(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8", "replace")).hexdigest()[:16]


def _call_key(name: str, arguments: str) -> str:
    try:
        args = json.dumps(json.loads(arguments or "{}"), sort_keys=True)
    except ValueError:
        args = (arguments or "").strip()
    return name + " " + args


def error_signature(content: str) -> str | None:
    """The normalized error lines of a failed tool result, or None when it
    has none. Numbers, addresses, and temporary paths are removed, so that the
    same error from two attempts has the same signature."""
    lines = []
    for line in content.splitlines():
        line = line.strip()
        if line.startswith("exit_code:") or not ERROR_LINE.search(line):
            continue
        lines.append(VOLATILE.sub("#", line)[:200])
    return "\n".join(lines[-5:]) or None


def environment_line(content: str) -> tuple[int, str] | None:
    """The first environment pattern in content: its index and the line."""
    for line in content.splitlines():
        for i, p in enumerate(ENVIRONMENT_PATTERNS):
            if p.search(line):
                return i, line.strip()[:300]
    return None


class Guards:
    """The guards of one run. The loop calls step() before each model call
    and tool() after each tool result."""

    def __init__(self, revise: bool = False):
        self.no_edit_limit = NO_EDIT_STEPS_REVISE if revise else NO_EDIT_STEPS
        self.calls: deque[tuple[str, str]] = deque(maxlen=REPEAT_WINDOW)
        self.noted_repeat: set[str] = set()
        self.errors: dict[str, set[str]] = {}
        self.noted_error: set[str] = set()
        self.noted_env: set[int] = set()
        self.steps_since_edit = 0
        self.noted_no_edit = False
        self.thinking_cuts = 0

    def step(self) -> Action | None:
        """Count one model call. A run with no change to a file gets a note at
        the limit and ends at NO_EDIT_STOP times the limit."""
        self.steps_since_edit += 1
        n = self.steps_since_edit
        if n >= NO_EDIT_STOP * self.no_edit_limit:
            return Action(stop="no_progress", reason=f"no file changed in {n} steps")
        if n >= self.no_edit_limit and not self.noted_no_edit:
            self.noted_no_edit = True
            return Action(note=NO_EDIT.format(n=n), reason="no edit")
        return None

    def thinking_cut(self) -> Action | None:
        self.thinking_cuts += 1
        if self.thinking_cuts >= THINKING_CUTS_STOP:
            return Action(stop="stalled", reason=f"{self.thinking_cuts} replies cut while thinking")
        return None

    def tool(self, name: str, arguments: str, content: str, ok: bool) -> Action | None:
        """Look at one tool result. Returns the first rule that fires."""
        if name in EDIT_TOOLS and ok:
            self.steps_since_edit, self.noted_no_edit = 0, False
        key = _call_key(name, arguments)
        result = _digest(content)
        self.calls.append((key, result))
        same = sum(1 for k, r in self.calls if k == key and r == result)
        what = f"{name} with the same arguments" if name != "bash" else "the same command"
        if same >= REPEAT_STOP:
            return Action(stop="loop", reason=f"{what} ran {same} times with the same result")
        if same >= REPEAT_NOTE and key not in self.noted_repeat:
            self.noted_repeat.add(key)
            return Action(note=REPEAT.format(what=what, n=same), reason="repeat")
        if ok:
            return None
        if env := environment_line(content):
            if env[0] not in self.noted_env:
                self.noted_env.add(env[0])
                return Action(note=ENVIRONMENT.format(line=env[1]), reason="environment")
        sig = error_signature(content)
        if sig is None:
            return None
        keys = self.errors.setdefault(sig, set())
        keys.add(key)
        if len(keys) >= ERROR_STOP:
            return Action(stop="loop", reason=f"the same error from {len(keys)} different commands")
        if len(keys) >= ERROR_NOTE and sig not in self.noted_error:
            self.noted_error.add(sig)
            return Action(note=ERROR.format(n=len(keys), line=sig.splitlines()[-1]), reason="error repeat")
        return None
