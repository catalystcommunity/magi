"""Run the Go tests of the changed modules of each case with a database.

  python3 -I evals/coding/dbcheck.py RESULTS_DIR [--cases evals/coding/cases.jsonl]
      [--repo .] [--only pr6]

The eval runs each case at its base commit. The check.sh of an old base
can skip the database tests, so a change can pass the check and fail on a
real database (pass 3, pr6). This script applies the diff.patch of each
case to a worktree at the base, finds the Go modules of the changed files,
and runs their tests with tools/test-db.sh of the current repository.
It writes db.output.txt and db.json next to the diff. It needs initdb and
pg_ctl on PATH.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import tempfile

DIFF_FILE = re.compile(r"^diff --git a/(\S+) b/(\S+)$", re.M)


def changed_files(patch: str) -> list[str]:
    """The paths of the patch, both sides of a rename."""
    out = set()
    for a, b in DIFF_FILE.findall(patch):
        out.update((a, b))
    return sorted(out)


def go_modules(files, has_gomod) -> list[str]:
    """The nearest directory with go.mod of each file, in the repository.

    has_gomod(dir) tells if dir/go.mod exists in the worktree with the patch;
    the root directory does not count, because the repository has no root module.
    """
    mods = set()
    for f in files:
        d = os.path.dirname(f)
        while d:
            if has_gomod(d):
                mods.add(d)
                break
            d = os.path.dirname(d)
    return sorted(mods)


def test_command(worktree: str, modules) -> list[str]:
    """One shell for all modules, so one database start serves the case.
    Each module runs also when an earlier one fails."""
    script = ('s=0; for m in "$@"; do echo "== go test: $m"; '
              '(cd "$m" && GOWORK=off go test -count=1 ./...) || { echo "FAIL: $m"; s=1; }; done; exit $s')
    return ["sh", "-c", script, "sh", *[os.path.join(worktree, m) for m in modules]]


def git(repo, *args, **kw):
    return subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True, text=True, **kw)


def check_case(repo, case, case_dir, run=subprocess.run) -> dict:
    patch_path = os.path.join(case_dir, "diff.patch")
    patch = ""
    if os.path.exists(patch_path):
        with open(patch_path) as f:
            patch = f.read()
    if not patch.strip():
        return {"outcome": "no-diff", "modules": []}
    with tempfile.TemporaryDirectory(prefix="dbcheck-") as tmp:
        wt = os.path.join(tmp, "wt")
        git(repo, "worktree", "add", "-q", "--detach", wt, case["base"])
        try:
            git(wt, "apply", "--whitespace=nowarn", os.path.abspath(patch_path))
            mods = go_modules(changed_files(patch), lambda d: os.path.exists(os.path.join(wt, d, "go.mod")))
            if not mods:
                return {"outcome": "no-go-module", "modules": []}
            r = run([os.path.join(repo, "tools", "test-db.sh"), "--", *test_command(wt, mods)],
                    capture_output=True, text=True)
            with open(os.path.join(case_dir, "db.output.txt"), "w") as f:
                f.write(r.stdout + r.stderr)
            failed = re.findall(r"^FAIL: (.+)$", r.stdout, re.M)
            return {"outcome": "pass" if r.returncode == 0 else "fail", "modules": mods,
                    "failed": [os.path.relpath(m, wt) for m in failed]}
        finally:
            git(repo, "worktree", "remove", "--force", wt)


def main(argv=None, run=subprocess.run):
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("results")
    ap.add_argument("--cases", default=os.path.join(here, "cases.jsonl"))
    ap.add_argument("--repo", default=os.path.dirname(os.path.dirname(here)))
    ap.add_argument("--only", default="", help="comma-separated case ids")
    a = ap.parse_args(argv)
    want = {c for c in a.only.split(",") if c}
    with open(a.cases) as f:
        cases = [json.loads(line) for line in f if line.strip()]
    repo = os.path.abspath(a.repo)
    for case in cases:
        case_dir = os.path.join(a.results, case["id"])
        if not os.path.isdir(case_dir) or (want and case["id"] not in want):
            continue
        res = check_case(repo, case, case_dir, run=run)
        with open(os.path.join(case_dir, "db.json"), "w") as f:
            json.dump(res, f, indent=2)
        print(f"{case['id']:5} {res['outcome']:12} {' '.join(res['modules'])}"
              + (f"  failed: {' '.join(res['failed'])}" if res.get("failed") else ""), flush=True)


if __name__ == "__main__":
    sys.exit(main())
