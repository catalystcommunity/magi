#!/usr/bin/env python3
"""The comparison verdict for a change to a prompt or to the persona turn
(README.md, docs/decisions/0006).

It runs the turn eval on the baseline (a commit) and on the candidate (the
working tree), with the same model and the same set, and prints PASS or
FAIL. Exit status: 0 pass, 1 fail, 2 the eval did not run correctly.

  python3 -I evals/turn/verdict.py --base HEAD --url http://127.0.0.1:18092/v1 --model gemma-4-12b
"""
import argparse
import hashlib
import json
import math
import os
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HARNESS = "magi/compose/turneval_test.go"


def sh(cmd, cwd=None, env=None, check=True):
    p = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True)
    if check and p.returncode != 0:
        raise RuntimeError(f"{' '.join(cmd)}: {p.stderr.strip()[-2000:]}")
    return p


def run_eval(tree, args, out, log):
    """Runs TestTurnEval in a source tree. The results go to out."""
    env = dict(os.environ, MAGI_TURN_EVAL_URL=args.url, MAGI_TURN_EVAL_MODEL=args.model,
               MAGI_TURN_EVAL_CASES=str(Path(args.set).resolve()), MAGI_TURN_EVAL_OUT=str(out),
               MAGI_TURN_EVAL_RUNS=str(args.runs), MAGI_TURN_EVAL_ONLY=args.only or "")
    # The path is absolute: tools/test-db.sh runs its command from its own
    # repository, which is the candidate.
    test = f"cd '{Path(tree).resolve()}/magi' && GOWORK=off go test -count=1 -timeout 0 ./compose/ -run '^TestTurnEval$' -v"
    cmd = ["sh", "-c", test] if os.environ.get("MAGI_TEST_DATABASE_URL") else [str(ROOT / "tools/test-db.sh"), "--", "sh", "-c", test]
    p = subprocess.run(cmd, cwd=tree, env=env, capture_output=True, text=True)
    Path(log).write_text(p.stdout + p.stderr)
    if p.returncode != 0 or "--- SKIP" in p.stdout or not Path(out).exists():
        raise RuntimeError(f"the eval did not run in {tree}; see {log}")


def load(path):
    """Returns {case: [result of each run]} and the core cases."""
    by, core = {}, set()
    for line in Path(path).read_text().splitlines():
        if not line.strip():
            continue
        r = json.loads(line)
        by.setdefault(r["case"], []).append(r)
        if r.get("core"):
            core.add(r["case"])
    return by, core


def compare(base, cand, core):
    """The verdict from the results of the baseline and of the candidate.

    Rules:
    - A run with an error of the model service is not a result: the
      verdict is "error".
    - Core floor: a core case that the baseline passes in each run, and
      that the candidate fails in half of its runs or more, fails the
      verdict.
    - Total: the candidate fails when it passes fewer runs than the
      baseline, by more than the noise. The noise is the largest
      difference of the totals between two runs of the baseline, and at
      least 1.
    """
    out = {"verdict": "pass", "reasons": [], "cases": {}}
    for name, side in (("baseline", base), ("candidate", cand)):
        bad = sorted(c for c, rs in side.items() if any(r.get("model_errors") for r in rs))
        if bad:
            out["verdict"] = "error"
            out["reasons"].append(f"{name}: the model service failed in {', '.join(bad)}")
    if set(base) != set(cand) or any(len(base[c]) != len(cand[c]) for c in base if c in cand):
        out["verdict"] = "error"
        out["reasons"].append("the baseline and the candidate did not run the same cases and runs")
    if out["verdict"] == "error":
        return out
    rate = lambda rs: sum(r["pass"] for r in rs) / len(rs)
    for c in sorted(base):
        b, k = rate(base[c]), rate(cand[c])
        out["cases"][c] = {"core": c in core, "baseline": b, "candidate": k}
        if c in core and b == 1.0 and k <= 0.5:
            out["verdict"] = "fail"
            out["reasons"].append(f"core case {c}: the baseline passes each run, the candidate passes {k:.0%}")
    runs = len(next(iter(base.values()))) if base else 0
    totals = [sum(base[c][i]["pass"] for c in base) for i in range(runs)]
    noise = max(1, max(totals) - min(totals)) if totals else 1
    tb = sum(r["pass"] for rs in base.values() for r in rs)
    tc = sum(r["pass"] for rs in cand.values() for r in rs)
    # The noise is for one run; the totals are of all runs.
    limit = noise * math.sqrt(runs) if runs else noise
    out.update(baseline_total=tb, candidate_total=tc, of=sum(len(rs) for rs in base.values()), noise=round(limit, 2))
    if tc < tb - limit:
        out["verdict"] = "fail"
        out["reasons"].append(f"total: the candidate passes {tc}, the baseline {tb}; the noise is {limit:.1f}")
    elif tc > tb + limit:
        out["reasons"].append(f"total: better ({tc} against {tb})")
    elif out["verdict"] == "pass":
        out["reasons"].append(f"total: no change ({tc} against {tb}; the noise is {limit:.1f})")
    return out


def report(v):
    lines = []
    for c, r in v.get("cases", {}).items():
        mark = "core" if r["core"] else "    "
        arrow = "worse" if r["candidate"] < r["baseline"] else "better" if r["candidate"] > r["baseline"] else ""
        lines.append(f"  {c:6} {mark}  baseline {r['baseline']:4.0%}  candidate {r['candidate']:4.0%}  {arrow}")
    lines += [f"  {r}" for r in v["reasons"]]
    lines.append("VERDICT: " + v["verdict"].upper())
    return "\n".join(lines)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--base", default="HEAD", help="the commit of the baseline (default HEAD)")
    ap.add_argument("--set", default=str(ROOT / "evals/turn/core-v1.json"))
    ap.add_argument("--url", required=True, help="an OpenAI-compatible model server that serves no other traffic")
    ap.add_argument("--model", required=True)
    ap.add_argument("--runs", type=int, default=3)
    ap.add_argument("--only", help="case ids, with commas")
    ap.add_argument("--out", default=str(ROOT / "evals/turn/results"))
    ap.add_argument("--label", default="")
    args = ap.parse_args(argv)
    sha = sh(["git", "rev-parse", "--short=12", args.base], cwd=ROOT).stdout.strip()
    set_hash = hashlib.sha256(Path(args.set).read_bytes()).hexdigest()[:12]
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    # The results of a baseline are good for the same commit, set, model,
    # and runs: a second candidate does not run the baseline again.
    stem = f"{sha}-{set_hash}-{args.model}-r{args.runs}" + (f"-{args.only.replace(',', '+')}" if args.only else "")
    base_file = out / f"baseline-{stem}.jsonl"
    try:
        if not base_file.exists():
            with tempfile.TemporaryDirectory(prefix="turn-eval-") as tmp:
                tree = Path(tmp) / "base"
                sh(["git", "worktree", "add", "--detach", str(tree), sha], cwd=ROOT)
                try:
                    if not (tree / HARNESS).exists():
                        raise RuntimeError(f"the baseline {sha} is older than the turn eval ({HARNESS})")
                    part = base_file.with_suffix(".part")
                    run_eval(tree, args, part, out / f"baseline-{stem}.log")
                    part.rename(base_file)
                finally:
                    sh(["git", "worktree", "remove", "--force", str(tree)], cwd=ROOT, check=False)
        label = args.label or "candidate"
        cand_file = out / f"{label}-on-{stem}.jsonl"
        run_eval(ROOT, args, cand_file, out / f"{label}-on-{stem}.log")
        base, core = load(base_file)
        cand, core2 = load(cand_file)
        v = compare(base, cand, core | core2)
    except RuntimeError as e:
        print(f"verdict: {e}", file=sys.stderr)
        print("VERDICT: ERROR")
        return 2
    v.update(base=sha, set=Path(args.set).name, set_sha256=set_hash, model=args.model, runs=args.runs)
    (out / f"{label}-on-{stem}.verdict.json").write_text(json.dumps(v, indent=1) + "\n")
    print(report(v))
    return {"pass": 0, "fail": 1}.get(v["verdict"], 2)


if __name__ == "__main__":
    sys.exit(main())
