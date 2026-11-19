"""Tests of evals/coding/dbcheck.py: a real git repository, a fake test-db.sh."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import dbcheck as D  # noqa: E402


def sh(cwd, *args):
    return subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True).stdout


class TestChangedFiles(unittest.TestCase):
    def test_both_sides_of_a_rename(self):
        patch = "diff --git a/x/old.go b/x/new.go\nrename from x/old.go\ndiff --git a/README.md b/README.md\n"
        self.assertEqual(D.changed_files(patch), ["README.md", "x/new.go", "x/old.go"])


class TestGoModules(unittest.TestCase):
    def test_the_nearest_module_and_no_root_module(self):
        mods = {"primitives/knowledge", "magi"}
        got = D.go_modules(["primitives/knowledge/store/sql.go", "magi/compose/a.go", "docs/x.md", "go.work"],
                           lambda d: d in mods)
        self.assertEqual(got, ["magi", "primitives/knowledge"])


class TestCheckCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.repo = os.path.join(self.tmp.name, "repo")
        os.makedirs(os.path.join(self.repo, "prim", "store"))
        Path(self.repo, "prim", "go.mod").write_text("module example.local/prim\n")
        Path(self.repo, "prim", "store", "a.go").write_text("package store\n")
        sh(self.repo, "git", "init", "-q")
        sh(self.repo, "git", "-c", "user.name=t", "-c", "user.email=t@example.local", "add", "-A")
        sh(self.repo, "git", "-c", "user.name=t", "-c", "user.email=t@example.local", "commit", "-q", "-m", "base")
        self.base = sh(self.repo, "git", "rev-parse", "HEAD").strip()
        Path(self.repo, "prim", "store", "a.go").write_text("package store\n\nconst X = 1\n")
        self.patch = sh(self.repo, "git", "diff")
        sh(self.repo, "git", "checkout", "-q", ".")
        self.case_dir = os.path.join(self.tmp.name, "results", "pr9")
        os.makedirs(self.case_dir)
        Path(self.case_dir, "diff.patch").write_text(self.patch)

    def tearDown(self):
        self.tmp.cleanup()

    def test_a_failed_module_with_the_patch_applied(self):
        seen = {}

        def fake_run(cmd, **kw):
            seen["cmd"] = cmd
            module = cmd[-1]
            seen["patched"] = Path(module, "store", "a.go").read_text()
            return subprocess.CompletedProcess(cmd, 1, stdout=f"== go test: {module}\nFAIL: {module}\n", stderr="")

        res = D.check_case(self.repo, {"id": "pr9", "base": self.base}, self.case_dir, run=fake_run)
        self.assertEqual(res, {"outcome": "fail", "modules": ["prim"], "failed": ["prim"]})
        self.assertEqual(seen["cmd"][:2], [os.path.join(self.repo, "tools", "test-db.sh"), "--"])
        self.assertIn("const X = 1", seen["patched"])
        self.assertIn("FAIL:", Path(self.case_dir, "db.output.txt").read_text())
        # The worktree is gone after the case.
        self.assertEqual(len(sh(self.repo, "git", "worktree", "list").splitlines()), 1)

    def test_the_main_writes_db_json_for_each_case(self):
        cases = Path(self.tmp.name, "cases.jsonl")
        cases.write_text(json.dumps({"id": "pr9", "base": self.base}) + "\n"
                         + json.dumps({"id": "pr8", "base": self.base}) + "\n")
        ok = lambda cmd, **kw: subprocess.CompletedProcess(cmd, 0, stdout="ok\n", stderr="")  # noqa: E731
        D.main([os.path.join(self.tmp.name, "results"), "--cases", str(cases), "--repo", self.repo], run=ok)
        self.assertEqual(json.loads(Path(self.case_dir, "db.json").read_text())["outcome"], "pass")
        self.assertFalse(Path(self.tmp.name, "results", "pr8").exists())

    def test_an_empty_diff_runs_nothing(self):
        Path(self.case_dir, "diff.patch").write_text("")

        def no_run(cmd, **kw):
            raise AssertionError("test-db.sh must not run")

        res = D.check_case(self.repo, {"id": "pr9", "base": self.base}, self.case_dir, run=no_run)
        self.assertEqual(res["outcome"], "no-diff")


if __name__ == "__main__":
    unittest.main()
