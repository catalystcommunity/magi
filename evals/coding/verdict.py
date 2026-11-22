"""The comparison verdict of the coding eval: pass, fail, or no change.

  python3 -I evals/coding/verdict.py --baseline LABEL,LABEL --candidate LABEL,LABEL
      [--results evals/coding/results] [--cases pr2,pr3]

A label is one run of run.py with probe.py after it: results/<label>/<case>/probe.json.
The baseline labels are runs of the version before the change. The candidate
labels are runs of the version with the change, with the same model. The
score of a run is the sum of the parts of the probes that pass, one part for
each case, so each case counts the same. A case with no diff has the score 0.

- fail: a probe passes in each baseline run and fails in each candidate run
  (the floor), or the candidate mean is below the baseline mean by more than
  the spread of the baseline runs.
- pass: the candidate mean is above the baseline mean by more than the
  spread of the baseline runs.
- no change: the difference is not larger than the spread.
- no verdict: fewer than two baseline runs, so there is no spread.

The exit status is 0 for pass, 1 for fail, 2 for no change, 3 for no verdict.
"""
import argparse
import json
import os
import sys


def load(results: str, label: str) -> dict[str, dict]:
    """Case id -> {"passed": set, "failed": set} for one run. A case that
    has a result and no probe result has empty sets."""
    out = {}
    root = os.path.join(results, label)
    for case in sorted(os.listdir(root)):
        d = os.path.join(root, case)
        if not os.path.isfile(os.path.join(d, "result.json")):
            continue
        res = {"passed": set(), "failed": set()}
        p = os.path.join(d, "probe.json")
        if os.path.isfile(p):
            with open(p) as f:
                j = json.load(f)
            res = {"passed": set(j.get("passed") or []), "failed": {x["probe"] for x in j.get("failed") or []}}
        out[case] = res
    return out


def compare(baseline: list[dict], candidate: list[dict], cases: list[str] | None = None) -> dict:
    """The verdict for the runs of the baseline and of the candidate."""
    runs = baseline + candidate
    ids = sorted(set.intersection(*(set(r) for r in runs))) if runs else []
    if cases:
        ids = [c for c in ids if c in cases]
    # The probes of a case: a run with no diff does not name them.
    probes = {c: set().union(*(r[c]["passed"] | r[c]["failed"] for r in runs)) for c in ids}
    ids = [c for c in ids if probes[c]]

    def score(run: dict) -> float:
        return sum(len(run[c]["passed"]) / len(probes[c]) for c in ids)

    def mean(xs: list[float]) -> float:
        return sum(xs) / len(xs) if xs else 0.0
    b, c = [score(r) for r in baseline], [score(r) for r in candidate]
    floor = sorted(f"{case}:{p}" for case in ids for p in probes[case]
                   if baseline and candidate and all(p in r[case]["passed"] for r in baseline)
                   and not any(p in r[case]["passed"] for r in candidate))
    by_case = {case: (mean([len(r[case]["passed"]) / len(probes[case]) for r in baseline]),
                      mean([len(r[case]["passed"]) / len(probes[case]) for r in candidate])) for case in ids}
    out = {"cases": ids, "baseline": b, "candidate": c, "floor": floor, "by_case": by_case,
           "spread": max(b) - min(b) if b else 0.0, "difference": mean(c) - mean(b)}
    if len(b) < 2 or not c or not ids:
        out["verdict"] = "no verdict"
    elif floor or out["difference"] < -out["spread"]:
        out["verdict"] = "fail"
    elif out["difference"] > out["spread"]:
        out["verdict"] = "pass"
    else:
        out["verdict"] = "no change"
    return out


def report(v: dict) -> str:
    lines = [f"verdict: {v['verdict']}",
             f"runs: {len(v['baseline'])} of the baseline, {len(v['candidate'])} of the candidate; "
             f"cases: {', '.join(v['cases']) or 'none'}",
             "baseline scores:  " + " ".join(f"{x:.2f}" for x in v["baseline"]),
             "candidate scores: " + " ".join(f"{x:.2f}" for x in v["candidate"]),
             f"difference of the means: {v['difference']:+.2f}; spread of the baseline runs: {v['spread']:.2f}"]
    lines += [f"  {case:5} {b:.2f} -> {c:.2f}" for case, (b, c) in v["by_case"].items()]
    if v["floor"]:
        lines.append("probes that pass in each baseline run and fail in each candidate run:")
        lines += ["  " + p for p in v["floor"]]
    if v["verdict"] == "no verdict":
        lines.append("A verdict needs two or more baseline runs, one or more candidate runs, and a case with "
                     "probes in each run.")
    return "\n".join(lines)


def main(argv=None) -> int:
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--baseline", required=True, help="comma-separated labels of the runs before the change")
    ap.add_argument("--candidate", required=True, help="comma-separated labels of the runs with the change")
    ap.add_argument("--results", default=os.path.join(here, "results"))
    ap.add_argument("--cases", default="", help="comma-separated case ids; the default is the cases in each run")
    args = ap.parse_args(argv)
    v = compare([load(args.results, x) for x in args.baseline.split(",") if x],
                [load(args.results, x) for x in args.candidate.split(",") if x],
                [c for c in args.cases.split(",") if c] or None)
    print(report(v))
    return {"pass": 0, "fail": 1, "no change": 2, "no verdict": 3}[v["verdict"]]


if __name__ == "__main__":
    sys.exit(main())
