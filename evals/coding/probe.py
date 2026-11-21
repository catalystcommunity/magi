"""Run the behavior probes of each case on the change of a candidate.

  python3 -I evals/coding/probe.py RESULTS_DIR [--cases evals/coding/cases.jsonl]
      [--repo .] [--only pr7] [--approved]

A probe is one test of one sentence of the task, through code that exists
at the base of the case (probes/<id>.py, probes/<id>_test.go). The first
line of a probe file names the directory where it runs. This script applies
the diff.patch of each case to a worktree at the base, copies the probe
files in, runs each probe, and writes probe.json next to the diff: the
probes that pass, the probes that fail with the sentence of each, the tests
of the base that the change removed, and the files that the change touches
and the approved change does not.

With --approved, the worktree is the approved commit of the case, and
RESULTS_DIR gets the result. Each probe must pass there.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

TARGET = re.compile(r"Target: ([\w./-]+)")
PY_TEST = re.compile(r"^(test_\w+) \(([\w.]+)\)\n?(.*?) \.\.\. (ok|FAIL|ERROR|skipped.*)$", re.M)
GO_TEST = re.compile(r"^func (TestProbe\w+)\(", re.M)
GO_DOC = re.compile(r"((?:^//.*\n)+)func (TestProbe\w+)\(", re.M)
TEST_NAME = re.compile(r"^\s*(?:def (test_\w+)\(|func (Test\w+)\()", re.M)
DIFF_FILE = re.compile(r"^diff --git a/(\S+) b/(\S+)$", re.M)


def probe_files(probes_dir: str, case_id: str) -> list[tuple[str, str]]:
    """(path, target directory) of each probe file of the case."""
    out = []
    for name in (case_id + ".py", case_id + "_test.go"):
        path = os.path.join(probes_dir, name)
        if os.path.exists(path):
            with open(path) as f:
                m = TARGET.search(f.readline())
            if not m:
                raise SystemExit(f"{path}: the first line has no Target")
            out.append((path, m.group(1)))
    return out


def parse_python(output: str) -> dict[str, tuple[bool, str]]:
    """unittest -v output: test name -> (passed, first line of the docstring)."""
    return {m.group(1): (m.group(4) == "ok", m.group(3).strip()) for m in PY_TEST.finditer(output)}


def go_probes(source: str) -> dict[str, str]:
    """Probe test name -> its doc comment in one line."""
    docs = {m.group(2): " ".join(x.lstrip("/ ").strip() for x in m.group(1).splitlines())
            for m in GO_DOC.finditer(source)}
    return {name: docs.get(name, "") for name in GO_TEST.findall(source)}


def test_names(text: str) -> set[str]:
    return {a or b for a, b in TEST_NAME.findall(text)}


def removed_tests(base_files: dict[str, str], new_files: dict[str, str | None]) -> list[str]:
    """The tests of the base that the change removed, as file::name."""
    out = []
    for path, old in base_files.items():
        new = new_files.get(path)
        gone = test_names(old) - (test_names(new) if new is not None else set())
        out += [f"{path}::{n}" for n in sorted(gone)]
    return sorted(out)


def git(repo, *args, **kw):
    return subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True, text=True, **kw)


def run_probes(wt: str, files, run=subprocess.run) -> tuple[list[str], list[dict], str]:
    passed, failed, log = [], [], ""
    for path, target in files:
        name = "probe_" + os.path.basename(path)
        shutil.copy(path, os.path.join(wt, target, name))
        if path.endswith(".py"):
            r = run([sys.executable, "-m", "unittest", "-v", name[:-3]], cwd=os.path.join(wt, target),
                    capture_output=True, text=True, timeout=600)
            log += r.stdout + r.stderr
            res = parse_python(r.stderr)
            if not res:
                failed.append({"probe": name, "sentence": "The probe file did not run.",
                               "detail": (r.stderr or r.stdout)[-600:]})
            for test, (ok, doc) in sorted(res.items()):
                if ok:
                    passed.append(test)
                else:
                    failed.append({"probe": test, "sentence": doc})
        else:
            with open(path) as f:
                probes = go_probes(f.read())
            for test, doc in sorted(probes.items()):
                # One process for each probe: a panic must not hide the others.
                r = run(["go", "test", "-count=1", "-run", f"^{test}$", "."], cwd=os.path.join(wt, target),
                        capture_output=True, text=True, timeout=900, env={**os.environ, "GOWORK": "off"})
                log += f"== {test}\n" + r.stdout + r.stderr
                if r.returncode == 0:
                    passed.append(test)
                else:
                    failed.append({"probe": test, "sentence": doc})
    return passed, failed, log


def check_case(repo, case, case_dir, probes_dir, approved=False, run=subprocess.run) -> dict:
    files = probe_files(probes_dir, case["id"])
    if not files:
        return {"outcome": "no-probes"}
    patch_path = os.path.join(case_dir, "diff.patch")
    patch = ""
    if not approved:
        if os.path.exists(patch_path):
            with open(patch_path) as f:
                patch = f.read()
        if not patch.strip():
            return {"outcome": "no-diff"}
    with tempfile.TemporaryDirectory(prefix="probe-") as tmp:
        wt = os.path.join(tmp, "wt")
        git(repo, "worktree", "add", "-q", "--detach", wt, case["approved"] if approved else case["base"])
        try:
            if approved:
                patch = git(repo, "diff", case["base"], case["approved"]).stdout
            else:
                git(wt, "apply", "--whitespace=nowarn", os.path.abspath(patch_path))
            touched = sorted({p for pair in DIFF_FILE.findall(patch) for p in pair})
            tests = [p for p in git(repo, "ls-tree", "-r", "--name-only", case["base"]).stdout.split("\n")
                     if p in touched and (os.path.basename(p).startswith("test_") or p.endswith("_test.go"))]
            base = {p: git(repo, "show", f"{case['base']}:{p}").stdout for p in tests}
            new = {}
            for p in tests:
                full = os.path.join(wt, p)
                if os.path.exists(full):
                    with open(full) as f:
                        new[p] = f.read()
            passed, failed, log = run_probes(wt, files, run=run)
        finally:
            git(repo, "worktree", "remove", "--force", wt)
    os.makedirs(case_dir, exist_ok=True)
    with open(os.path.join(case_dir, "probe.output.txt"), "w") as f:
        f.write(log)
    return {"outcome": "pass" if not failed else "fail", "passed": passed, "failed": failed,
            "removed_tests": removed_tests(base, new),
            "other_files": [p for p in touched if p not in case.get("changed", [])]}


def main(argv=None, run=subprocess.run):
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("results")
    ap.add_argument("--cases", default=os.path.join(here, "cases.jsonl"))
    ap.add_argument("--probes", default=os.path.join(here, "probes"))
    ap.add_argument("--repo", default=os.path.dirname(os.path.dirname(here)))
    ap.add_argument("--only", default="", help="comma-separated case ids")
    ap.add_argument("--approved", action="store_true", help="run the probes on the approved change")
    a = ap.parse_args(argv)
    want = {c for c in a.only.split(",") if c}
    with open(a.cases) as f:
        cases = [json.loads(line) for line in f if line.strip()]
    repo = os.path.abspath(a.repo)
    bad = 0
    for case in cases:
        case_dir = os.path.join(a.results, case["id"])
        if (not a.approved and not os.path.isdir(case_dir)) or (want and case["id"] not in want):
            continue
        res = check_case(repo, case, case_dir, a.probes, approved=a.approved, run=run)
        if res["outcome"] in ("no-probes", "no-diff"):
            print(f"{case['id']:5} {res['outcome']}", flush=True)
            continue
        with open(os.path.join(case_dir, "probe.json"), "w") as f:
            json.dump(res, f, indent=2)
        n = len(res["passed"]) + len(res["failed"])
        print(f"{case['id']:5} {len(res['passed'])} of {n} probes"
              + (f", removed tests: {len(res['removed_tests'])}" if res["removed_tests"] else ""), flush=True)
        for x in res["failed"]:
            print(f"      FAIL {x['probe']}: {x['sentence']}", flush=True)
        bad += bool(res["failed"])
    return 1 if a.approved and bad else 0


if __name__ == "__main__":
    sys.exit(main())
