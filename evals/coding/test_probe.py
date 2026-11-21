"""Tests of evals/coding/probe.py: a real git repository and a real probe run."""
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import probe as P  # noqa: E402

PROBE = '''"""Probes of case c1. Target: pkg/tests"""
import unittest

import mod


class C1(unittest.TestCase):
    def test_double(self):
        """Double the number."""
        self.assertEqual(mod.f(2), 4)

    def test_zero(self):
        """Zero stays zero."""
        self.assertEqual(mod.f(0), 0)
'''


def sh(cwd, *args, **kw):
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    return subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True, env=env, **kw).stdout


class TestParse(unittest.TestCase):
    def test_python_output_gives_the_result_and_the_sentence(self):
        out = ("test_a (probe_x.X.test_a)\nThe first sentence. ... ok\n"
               "test_b (probe_x.X.test_b)\nThe second sentence. ... FAIL\n"
               "test_c (probe_x.X.test_c) ... ERROR\n")
        self.assertEqual(P.parse_python(out), {"test_a": (True, "The first sentence."),
                                               "test_b": (False, "The second sentence."),
                                               "test_c": (False, "")})

    def test_go_probes_with_their_comments(self):
        src = ("package x\n\n// Add the outputs.\n// Second line.\nfunc TestProbeA(t *testing.T) {\n}\n\n"
               "func helper() {}\n\nfunc TestProbeB(t *testing.T) {\n}\n\nfunc TestOther(t *testing.T) {}\n")
        self.assertEqual(P.go_probes(src), {"TestProbeA": "Add the outputs. Second line.", "TestProbeB": ""})

    def test_removed_tests(self):
        base = {"a/test_x.py": "def test_one(self):\n    pass\n    def test_two(self):\n", "b/y_test.go": "func TestY(t *testing.T) {\n"}
        new = {"a/test_x.py": "def test_one(self):\n", "b/y_test.go": None}
        self.assertEqual(P.removed_tests(base, new), ["a/test_x.py::test_two", "b/y_test.go::TestY"])

    def test_a_probe_file_needs_a_target(self):
        with tempfile.TemporaryDirectory() as d:
            Path(d, "c1.py").write_text('"""No directory here."""\n')
            with self.assertRaises(SystemExit):
                P.probe_files(d, "c1")
            self.assertEqual(P.probe_files(d, "c2"), [])


class TestCheckCase(unittest.TestCase):
    """One case in a real repository: the base, a candidate patch, the approved commit."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.repo = os.path.join(self.tmp.name, "repo")
        tests = Path(self.repo, "pkg", "tests")
        tests.mkdir(parents=True)
        Path(tests, "mod.py").write_text("def f(x):\n    return x\n")
        Path(tests, "test_mod.py").write_text("def test_old():\n    pass\n\n\ndef test_keep():\n    pass\n")
        sh(self.repo, "git", "init", "-q")
        sh(self.repo, "git", "add", "-A")
        commit = ("git", "-c", "user.email=a@example.local", "-c", "user.name=t", "commit", "-qm")
        sh(self.repo, *commit, "base")
        base = sh(self.repo, "git", "rev-parse", "HEAD").strip()
        # The candidate: wrong for 2, and it removes a test and touches a file outside the case.
        Path(tests, "mod.py").write_text("def f(x):\n    return x * 3\n")
        Path(tests, "test_mod.py").write_text("def test_keep():\n    pass\n")
        Path(self.repo, "other.txt").write_text("x\n")
        sh(self.repo, "git", "add", "-A")
        self.case_dir = os.path.join(self.tmp.name, "results", "c1")
        os.makedirs(self.case_dir)
        Path(self.case_dir, "diff.patch").write_text(sh(self.repo, "git", "diff", "--cached"))
        sh(self.repo, "git", "reset", "-q", "--hard")
        Path(tests, "mod.py").write_text("def f(x):\n    return x * 2\n")
        sh(self.repo, "git", "add", "-A")
        sh(self.repo, *commit, "approved")
        self.case = {"id": "c1", "base": base, "approved": sh(self.repo, "git", "rev-parse", "HEAD").strip(),
                     "changed": ["pkg/tests/mod.py", "pkg/tests/test_mod.py"]}
        self.probes = os.path.join(self.tmp.name, "probes")
        os.makedirs(self.probes)
        Path(self.probes, "c1.py").write_text(PROBE)

    def tearDown(self):
        self.tmp.cleanup()

    def test_a_candidate_with_a_defect(self):
        res = P.check_case(self.repo, self.case, self.case_dir, self.probes)
        self.assertEqual(res["outcome"], "fail")
        self.assertEqual(res["passed"], ["test_zero"])
        self.assertEqual(res["failed"], [{"probe": "test_double", "sentence": "Double the number."}])
        self.assertEqual(res["removed_tests"], ["pkg/tests/test_mod.py::test_old"])
        self.assertEqual(res["other_files"], ["other.txt"])
        self.assertIn("test_double", Path(self.case_dir, "probe.output.txt").read_text())
        # the worktree is removed, and the repository is as before
        self.assertEqual(sh(self.repo, "git", "worktree", "list").count("\n"), 1)
        self.assertEqual(sh(self.repo, "git", "status", "--short"), "")

    def test_the_approved_change_passes(self):
        out = os.path.join(self.tmp.name, "approved", "c1")
        res = P.check_case(self.repo, self.case, out, self.probes, approved=True)
        self.assertEqual((res["outcome"], res["passed"], res["failed"]), ("pass", ["test_double", "test_zero"], []))

    def test_no_diff_and_no_probes(self):
        Path(self.case_dir, "diff.patch").write_text("")
        self.assertEqual(P.check_case(self.repo, self.case, self.case_dir, self.probes), {"outcome": "no-diff"})
        self.assertEqual(P.check_case(self.repo, {**self.case, "id": "c9"}, self.case_dir, self.probes),
                         {"outcome": "no-probes"})


if __name__ == "__main__":
    unittest.main()
