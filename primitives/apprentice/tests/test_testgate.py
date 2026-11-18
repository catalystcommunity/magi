import os
import subprocess
import tempfile
import unittest

from fakes import FakeModel, call, reply

from magi_apprentice import testgate
from magi_apprentice.apprentice import Apprentice, Task
from magi_apprentice.transcript import Redactor, Transcript
from magi_apprentice.config import Limits
from magi_apprentice.local import LocalExecution
from magi_apprentice.tools import Tools, Workspace


def write(d, path, text):
    p = os.path.join(d, path)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w") as f:
        f.write(text)


def git(d, *args):
    subprocess.run(["git", "-C", d, "-c", "user.name=t", "-c", "user.email=t@example.local", *args],
                   check=True, capture_output=True)


TESTS = """import unittest

from calc import double


class DoubleTest(unittest.TestCase):
    def test_old(self):
        self.assertEqual(double(2), 4)
{new}

if __name__ == "__main__":
    unittest.main()
"""


class NewTestsTest(unittest.TestCase):
    def test_the_added_test_functions_by_file(self):
        diff = ("diff --git a/a_test.go b/a_test.go\n+++ b/a_test.go\n+func TestA(t *testing.T) {\n"
                " func TestOld(t *testing.T) {\n"
                "diff --git a/pkg/tests/test_x.py b/pkg/tests/test_x.py\n+++ b/pkg/tests/test_x.py\n"
                "+    def test_new(self):\n-    def test_gone(self):\n"
                "diff --git a/a.go b/a.go\n+++ b/a.go\n+func TestNotATest(t *testing.T) {\n")
        self.assertEqual(testgate.new_tests(diff), {"a_test.go": ["TestA"], "pkg/tests/test_x.py": ["test_new"]})

    def test_a_renamed_test_is_not_new(self):
        old = "class T:\n    def test_a(self):\n        x = 1\n"
        new = "class T:\n    def test_b(self):\n        x = 1\n\n    def test_c(self):\n        x = 2\n"
        self.assertEqual(testgate.drop_renames({"tests/test_t.py": ["test_b", "test_c"]},
                                               {"tests/test_t.py": old}, {"tests/test_t.py": new}),
                         {"tests/test_t.py": ["test_c"]})

    def test_passing_tests_in_the_output(self):
        out = ("test_a (tests.test_x.T.test_a) ... ok\ntest_b (tests.test_x.T.test_b) ... FAIL\n"
               "=== RUN   TestA\n--- PASS: TestA (0.00s)\n--- FAIL: TestB (0.00s)\n--- SKIP: TestC (0.00s)\n")
        self.assertEqual(testgate.passing(out), ["TestA", "test_a"])

    def test_go_tests_run_in_their_module(self):
        cmds = testgate._run_commands({"magi/runner/steps_test.go": ["TestA", "TestB"]},
                                      {"magi/go.mod", "magi/runner/steps.go"})
        self.assertEqual(cmds, ["(cd magi && GOWORK=off go test -count=1 -v -run '^(TestA|TestB)$' ./runner 2>&1)"])


class CheckInARepositoryTest(unittest.TestCase):
    """The new tests run on HEAD in a worktree; the work directory stays."""

    def test_only_a_test_that_passes_without_the_change(self):
        with tempfile.TemporaryDirectory() as d:
            write(d, "calc.py", "def double(x):\n    return x + x\n")
            write(d, "tests/test_calc.py", TESTS.format(new=""))
            git(d, "init", "-q")
            git(d, "add", "-A")
            git(d, "commit", "-qm", "base")
            write(d, "calc.py", "def double(x):\n    return x + x\n\n\ndef triple(x):\n    return 3 * x\n")
            write(d, "tests/test_calc.py", TESTS.format(new=(
                "\n    def test_double_of_three(self):\n        self.assertEqual(double(3), 6)\n"
                "\n    def test_triple(self):\n        from calc import triple\n"
                "        self.assertEqual(triple(2), 6)\n")))
            tools = Tools(Workspace(LocalExecution(), "l", d, Limits()))
            self.assertEqual(testgate.check(tools), ["test_double_of_three"])
            status = subprocess.run(["git", "-C", d, "status", "--short"], capture_output=True, text=True).stdout
            self.assertEqual(sorted(status.split()), ["M", "M", "calc.py", "tests/test_calc.py"])
            worktrees = subprocess.run(["git", "-C", d, "worktree", "list"], capture_output=True, text=True).stdout
            self.assertEqual(len(worktrees.splitlines()), 1)

    def test_no_new_test_runs_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            write(d, "calc.py", "x = 1\n")
            git(d, "init", "-q")
            git(d, "add", "-A")
            git(d, "commit", "-qm", "base")
            write(d, "calc.py", "x = 2\n")
            self.assertEqual(testgate.check(Tools(Workspace(LocalExecution(), "l", d, Limits()))), [])


class GateInRunTest(unittest.TestCase):
    def test_the_first_finish_gets_the_note_once(self):
        with tempfile.TemporaryDirectory() as d:
            write(d, "calc.py", "def double(x):\n    return x + x\n")
            write(d, "tests/test_calc.py", TESTS.format(new=""))
            git(d, "init", "-q")
            git(d, "add", "-A")
            git(d, "commit", "-qm", "base")
            write(d, "tests/test_calc.py", TESTS.format(new=(
                "\n    def test_double_of_three(self):\n        self.assertEqual(double(3), 6)\n")))
            lim = Limits(self_review=False)
            model = FakeModel([reply(call("finish", summary="added a test")),
                               reply(call("finish", summary="added a test of behavior that was there"))])
            tr = Transcript(Redactor())
            r = Apprentice(model, Tools(Workspace(LocalExecution(), "l", d, lim)), lim, tr).run(
                Task("Add a test of double.", workdir=d))
            self.assertEqual(r.summary, "added a test of behavior that was there")
            note = [m for m in model.requests[1] if m["role"] == "tool"][-1]["content"]
            self.assertEqual(note, testgate.note(["test_double_of_three"]))
            self.assertIn("pass on the code without your change", note)


class GateInRunTest(unittest.TestCase):
    def test_the_first_finish_gets_the_note_once(self):
        with tempfile.TemporaryDirectory() as d:
            write(d, "calc.py", "def double(x):\n    return x + x\n")
            write(d, "tests/test_calc.py", TESTS.format(new=""))
            git(d, "init", "-q")
            git(d, "add", "-A")
            git(d, "commit", "-qm", "base")
            write(d, "tests/test_calc.py", TESTS.format(new=(
                "\n    def test_double_of_three(self):\n        self.assertEqual(double(3), 6)\n")))
            lim = Limits(self_review=False)
            model = FakeModel([reply(call("finish", summary="added a test")),
                               reply(call("finish", summary="added a test of behavior that was there"))])
            tr = Transcript(Redactor())
            r = Apprentice(model, Tools(Workspace(LocalExecution(), "l", d, lim)), lim, tr).run(
                Task("Add a test of double.", workdir=d))
            self.assertEqual(r.summary, "added a test of behavior that was there")
            note = [m for m in model.requests[1] if m["role"] == "tool"][-1]["content"]
            self.assertEqual(note, testgate.note(["test_double_of_three"]))
            self.assertIn("pass on the code without your change", note)


if __name__ == "__main__":
    unittest.main()
