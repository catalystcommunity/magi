"""Measure the reviewer (primitives/apprentice/magi_apprentice/review.py)
against the verdicts of a person.

  python3 evals/review/run.py --url URL --token-file F --model NAME --cases CASES.jsonl
      --label L [--samples 3] [--only id1,id2] [--out evals/review/results]

URL is an OpenAI-compatible base URL (for example the runner model route of
Magi, https://magi.example.local/runner/v1, with a runner token). Each line of
CASES.jsonl is one decision of a person:

  {"id": "...", "task": "<instructions>", "patch": "<diff>",
   "label": "approve" | "revise", "comment": "<what the person wrote>"}

The script writes one result file for each case and summary.json. The
number that matters most is false approvals: cases that the person sent
back and the reviewer approved. Auto-approval needs 0 of them.
"""
import argparse
import json
import os
import sys
import time

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(ROOT, "primitives", "apprentice"))
from magi_apprentice.config import ModelConfig  # noqa: E402
from magi_apprentice.model import ChatModel  # noqa: E402
from magi_apprentice.review import review  # noqa: E402


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

    rows = []
    with open(a.cases) as f:
        cases = [json.loads(line) for line in f if line.strip()]
    for c in cases:
        if only and c["id"] not in only:
            continue
        t0 = time.monotonic()
        rv = review(model, c["task"], c["patch"], lessons, samples=a.samples)
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
