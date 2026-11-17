"""The supervisor: a second model that looks at a running task when a
trigger fires, and says continue, steer, stop, or escalate.

It does not run on every step. The triggers are the notes of the run guards
(a repeat, the same error, no edit, an environment error) and 25% and 50% of
the step budget. It gets a short picture of the run (the task, the last tool
calls with the start of their output, the diff stat, and the trigger), so one
call is a few thousand tokens. It should run on another engine than the
worker (for example another GPU): on the same engine, each call would throw
away the cached context of the worker.

An answer that cannot be parsed is escalate: a person looks, because the
supervisor could not.
"""
from __future__ import annotations

import json
import re
from dataclasses import dataclass

from .model import Model, ModelError

SUPERVISOR_SYSTEM = """You supervise a coding agent that works alone on one task in a sandbox. \
A trigger fired. Decide what happens next. Answer with one JSON object and nothing else:
{"verdict": "continue" | "steer" | "stop" | "escalate",
 "category": "progressing" | "exploring" | "looping" | "out_of_scope" | "blocked_environment" | "too_big" | "other",
 "message": "<one or two sentences>"}
- continue: the agent moves toward the task's result, or it reads code that the task needs.
- steer: the agent wastes steps (repeats, tries variants of a step that cannot work, works outside \
the task, explores without a plan). message is the instruction to the agent: what to stop and \
what to do instead.
- stop: the agent cannot finish: the environment blocks it (no network, a missing tool, no \
permission) and the task needs it, or the task is too big for the budget. message tells the person why.
- escalate: you cannot tell, or a person must decide (the task is unclear, or the agent wants to \
change something risky).
Prefer continue when the agent makes progress. Prefer steer over stop when a different approach can work."""

VERDICTS = ("continue", "steer", "stop", "escalate")
JSON_OBJECT = re.compile(r"\{.*\}", re.S)

# The budget points where the supervisor looks once, as fractions of max_steps.
CHECKPOINTS = (0.25, 0.5)
# The fewest steps between two supervisor calls.
MIN_GAP_STEPS = 5


@dataclass
class Decision:
    verdict: str
    category: str = "other"
    message: str = ""


def parse_decision(text: str) -> Decision:
    m = JSON_OBJECT.search(text or "")
    if not m:
        return Decision("escalate", "other", "the supervisor answer had no JSON object")
    try:
        data = json.loads(m.group(0))
    except ValueError:
        return Decision("escalate", "other", "the supervisor answer was not valid JSON")
    if not isinstance(data, dict) or data.get("verdict") not in VERDICTS:
        return Decision("escalate", "other", "the supervisor answer had no valid verdict")
    msg = str(data.get("message") or "").strip()[:600]
    if data["verdict"] == "steer" and not msg:
        return Decision("escalate", "other", "the supervisor said steer with no message")
    return Decision(data["verdict"], str(data.get("category") or "other"), msg)


def supervisor_prompt(task: str, trigger: str, step: int, max_steps: int, recent: list[str],
                      diff_stat: str) -> list[dict]:
    calls = "\n".join(recent[-15:]) or "(none)"
    user = (f"## Task\n\n{task.strip()[:3000]}\n\n## Trigger\n\n{trigger}\n\n"
            f"## Budget\n\nStep {step} of {max_steps}.\n\n## Last tool calls (oldest first)\n\n{calls}\n\n"
            f"## Files changed so far (git diff --stat)\n\n{diff_stat.strip() or '(no change yet)'}\n")
    return [{"role": "system", "content": SUPERVISOR_SYSTEM}, {"role": "user", "content": user}]


class Supervisor:
    """Decides when to ask, and asks. The loop calls due() at each step
    with the trigger of the step (or None), then ask() when due."""

    def __init__(self, model: Model, max_steps: int):
        self.model = model
        self.max_steps = max_steps
        self.last_step = -MIN_GAP_STEPS
        self.passed: set[float] = set()

    def due(self, step: int, trigger: str | None) -> str | None:
        """The trigger to ask about at this step, or None."""
        if step - self.last_step < MIN_GAP_STEPS:
            return None
        if trigger:
            return trigger
        for c in CHECKPOINTS:
            if c not in self.passed and step >= self.max_steps * c:
                self.passed.add(c)
                return f"checkpoint: {int(c * 100)}% of the step budget is used"
        return None

    def ask(self, step: int, task: str, trigger: str, recent: list[str], diff_stat: str) -> Decision:
        self.last_step = step
        try:
            reply = self.model.complete(supervisor_prompt(task, trigger, step, self.max_steps, recent, diff_stat), [])
        except ModelError as e:
            # A supervisor that is down must not stop the work: the guards
            # still hold the run.
            return Decision("continue", "other", f"the supervisor did not answer: {e}")
        return parse_decision(reply.content)
