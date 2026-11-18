"""Measure the reviewer (primitives/apprentice/magi_apprentice/review.py)
against the verdicts of a person.

  python3 evals/review/run.py --url URL --token-file F --model NAME --cases CASES.jsonl
      --label L [--samples 3] [--only id1,id2] [--out evals/review/results]
      [--agent | --lens] [--context] [--checked] [--repo PATH --worktrees DIR]

URL is an OpenAI-compatible base URL (for example the runner model route of
Magi, https://magi.example.local/runner/v1, with a runner token). Each line of
CASES.jsonl is one decision of a person:

  {"id": "...", "task": "<instructions>", "patch": "<diff>",
   "label": "approve" | "revise", "comment": "<what the person wrote>"}

The script writes one result file for each case and summary.json. The
number that matters most is false approvals: cases that the person sent
back and the reviewer approved. Auto-approval needs 0 of them.

With --agent, the tool-using reviewer (review_agent.py) reviews each case.
With --lens, each case gets one call for each review lens (review.py
LENSES). With --context, the review gets the whole changed functions and the
functions that they call (review_context.py). With --checked, the harness drops the blockers that the passing
checks disprove (review.py verify): the checks pass on each case of a
review step. The prompt does not say that the checks passed. These modes need a checkout:
a case needs "base", the commit that its patch applies to.
The script adds a git worktree of PATH at the base under DIR, applies the
patch, and removes the worktree after the case.
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import time

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(ROOT, "primitives", "apprentice"))
from magi_apprentice.config import ModelConfig  # noqa: E402
from magi_apprentice.model import ChatModel  # noqa: E402
from magi_apprentice.config import Limits  # noqa: E402
from magi_apprentice.local import LocalExecution  # noqa: E402
from magi_apprentice.review import Review, combine, lens_review, review  # noqa: E402
from magi_apprentice.review_context import review_context  # noqa: E402
from magi_apprentice.review_agent import agent_review  # noqa: E402
from magi_apprentice.tools import Tools, Workspace  # noqa: E402


def checkout(repo: str, worktrees: str, case: dict) -> str:
    wt = os.path.join(worktrees, case["id"])
    if os.path.exists(wt):
        subprocess.run(["git", "-C", repo, "worktree", "remove", "--force", wt], capture_output=True)
        shutil.rmtree(wt, ignore_errors=True)
    subprocess.run(["git", "-C", repo, "worktree", "add", "-q", "--detach", wt, case["base"]], check=True)
    subprocess.run(["git", "-C", wt, "apply", "--whitespace=nowarn", "-"], input=case["patch"].encode(), check=True)
    return wt


def checkout_case(model, a, case: dict, lessons: str, log) -> Review:
    wt = checkout(a.repo, a.worktrees, case)
    try:
        tools = Tools(Workspace(LocalExecution(), "local", wt, Limits()))
        if a.agent:
            return combine([agent_review(model, tools, case["task"], case["patch"], lessons,
                                         max_steps=a.max_steps, log=log) for _ in range(a.samples)])
        patch, context = case["patch"], ""
        if a.context:
            fdiff, context = review_context(tools)
            patch = fdiff or patch
        if a.lens:
            return combine([s for _ in range(a.samples)
                            for s in lens_review(model, case["task"], patch, lessons, context,
                                                 checked=a.checked).samples])
        return review(model, case["task"], patch, lessons, samples=a.samples, context=context,
                      checked=a.checked)
    finally:
        subprocess.run(["git", "-C", a.repo, "worktree", "remove", "--force", wt], capture_output=True)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--cases", required=True)
    ap.add_argument("--label", required=True)
    ap.add_argument("--samples", type=int, default=3)
    ap.add_argument("--temperature", type=float, default=0.6)
    ap.add_argument("--max-tokens", type=int, default=12000)
    ap.add_argument("--only", default="")
    ap.add_argument("--out", default=os.path.join(ROOT, "evals", "review", "results"))
    ap.add_argument("--agent", action="store_true", help="use the tool-using reviewer")
    ap.add_argument("--repo", default=ROOT)
    ap.add_argument("--worktrees", default="")
    ap.add_argument("--max-steps", type=int, default=25)
    ap.add_argument("--lens", action="store_true", help="one call for each review lens")
    ap.add_argument("--checked", action="store_true",
                    help="the checks passed on each case (a review step comes after the check step)")
    ap.add_argument("--context", action="store_true",
                    help="whole changed functions and the functions that they call")
    a = ap.parse_args()

    with open(a.token_file) as f:
        token = f.read().strip()
    model = ChatModel(ModelConfig(base_url=a.url, name=a.model, api_key=token, max_tokens=a.max_tokens,
                                  temperature=a.temperature))
    lessons_path = os.path.join(ROOT, "magi", "compose", "baseknowledge", "review-lessons.md")
    lessons = open(lessons_path).read() if os.path.exists(lessons_path) else ""
    only = {x for x in a.only.split(",") if x}
    out = os.path.join(a.out, a.label)
    os.makedirs(out, exist_ok=True)
    if not a.worktrees:
        a.worktrees = os.path.join(out, "worktrees")

    rows = []
    with open(a.cases) as f:
        cases = [json.loads(line) for line in f if line.strip()]
    for c in cases:
        if only and c["id"] not in only:
            continue
        t0 = time.monotonic()
        if a.agent or a.lens or a.context:
            rv = checkout_case(model, a, c, lessons, lambda line: print(line, flush=True))
        else:
            rv = review(model, c["task"], c["patch"], lessons, samples=a.samples, checked=a.checked)
        row = {"id": c["id"], "label": c["label"], "verdict": rv.verdict,
               "samples": [s.verdict for s in rv.samples], "seconds": round(time.monotonic() - t0, 1),
               "defects": rv.defects, "comment": c.get("comment", "")}
        rows.append(row)
        with open(os.path.join(out, c["id"] + ".json"), "w") as f:
            json.dump({**row, "answers": [s.text for s in rv.samples]}, f, indent=1)
        print(f"{c['id']}: label {c['label']}, reviewer {rv.verdict} {row['samples']} ({row['seconds']} s)", flush=True)

    def count(label, verdict):
        return sum(1 for r in rows if r["label"] == label and r["verdict"] == verdict)
    summary = {"label": a.label, "model": a.model, "samples": a.samples, "cases": len(rows),
               "true_approvals": count("approve", "approve"), "false_approvals": count("revise", "approve"),
               "true_revisions": count("revise", "revise"), "false_revisions": count("approve", "revise"),
               "invalid_samples": sum(r["samples"].count("invalid") for r in rows)}
    with open(os.path.join(out, "summary.json"), "w") as f:
        json.dump(summary, f, indent=1)
    print(json.dumps(summary))
    return 0


if __name__ == "__main__":
    sys.exit(main())
