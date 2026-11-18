"""The review of a model before the review of a person.

The check step runs it after the checks pass: the harness collects the
context of the change (review_context.py), each review lens looks at it
(review.py), and the harness drops the claims that the checks disprove.
The result goes into the review request as a hint. A person still decides:
on 38 cases of evals/review the reviewer approved 1 change that the person
sent back, and auto-approval needs 0.
"""
from __future__ import annotations

import shlex
import time

from .model import Model
from .review import combine, drop_generated, lens_review
from .review_context import review_context
from .tools import Tools, as_text

MAX_SHOWN_BLOCKERS = 8


def pre_review(model: Model, tools: Tools, task: str, base: str, lessons: str = "",
               samples: int = 1) -> dict:
    """The verdict of the lens reviewer on the change from base to the work
    directory, and its blockers. The checks of the change passed."""
    t0 = time.monotonic()
    patch, context = review_context(tools, base)
    if not patch:
        # A diff with whole functions that is too long: the plain diff, which
        # review_prompt cuts in the middle and says so.
        r = tools.ws.sh(f"git --no-pager diff {shlex.quote(base)} 2>/dev/null", timeout_s=60)
        patch, context = drop_generated(as_text(r.stdout)), ""
    if not patch.strip():
        return {"verdict": "revise", "blockers": [{"lens": "harness", "problem": "The change has no diff."}],
                "seconds": round(time.monotonic() - t0, 1)}
    rv = combine([s for _ in range(samples)
                  for s in lens_review(model, task, patch, lessons, context, checked=True).samples])
    blockers, seen = [], set()
    for s in rv.samples:
        if s.verdict == "invalid":
            blockers.append({"lens": s.text.split("\n", 1)[0].strip("[]"), "problem": "The answer had no verdict."})
        for d in s.defects:
            if not isinstance(d, dict) or d.get("disproved"):
                continue
            if d.get("severity") != "blocker" and not (d.get("lens") == "requirements" and s.verdict == "revise"):
                continue
            key = (d.get("file"), d.get("line"), d.get("lens"))
            if key not in seen:
                seen.add(key)
                blockers.append({k: d.get(k) for k in ("lens", "file", "line", "problem")})
    return {"verdict": rv.verdict, "blockers": blockers, "seconds": round(time.monotonic() - t0, 1)}


def summary(result: dict) -> str:
    """One short text for the review request of a person."""
    if result.get("error"):
        return f"The model review failed: {result['error']}"
    if result["verdict"] == "approve":
        return "The model review found no blocker."
    lines = [f"The model review found {len(result['blockers'])} possible blocker(s). Check them:"]
    for b in result["blockers"][:MAX_SHOWN_BLOCKERS]:
        where = f"{b.get('file')}:{b.get('line')}" if b.get("file") else ""
        lines.append(f"- [{b.get('lens')}] {where} {str(b.get('problem', ''))[:300]}".replace("  ", " "))
    if len(result["blockers"]) > MAX_SHOWN_BLOCKERS:
        lines.append(f"- and {len(result['blockers']) - MAX_SHOWN_BLOCKERS} more.")
    return "\n".join(lines)
