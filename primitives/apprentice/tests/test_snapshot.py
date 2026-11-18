import os
import subprocess
import tempfile
import unittest

from fakes import FakeModel, call, reply

from magi_apprentice.apprentice import Apprentice, Task
from magi_apprentice.config import Limits
from magi_apprentice.local import LocalExecution
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript


def git(d, *args):
    return subprocess.run(["git", "-C", d, "-c", "user.name=t", "-c", "user.email=t@example.local", *args],
                          check=True, capture_output=True, text=True).stdout


def read(d, name):
    with open(os.path.join(d, name)) as f:
        return f.read()


class SnapshotTest(unittest.TestCase):
    """A review round that ends without a passing finish gets the change
    that passed the check back."""

    def repo(self, d):
        with open(os.path.join(d, "a.py"), "w") as f:
            f.write("x = 1\n")
        git(d, "init", "-q")
        git(d, "add", "-A")
        git(d, "commit", "-qm", "base")

    def run_task(self, d, script, max_steps=4):
        lim = Limits(max_steps=max_steps, test_gate=False)
        tr = Transcript(Redactor())
        # The check passes while a.py has no "broken" in it.
        r = Apprentice(FakeModel(script), Tools(Workspace(LocalExecution(), "l", d, lim)), lim, tr).run(
            Task("Make x 2.", workdir=d, check_command="! grep -q broken a.py"))
        return r, tr

    def test_a_review_round_that_stops_at_a_limit_is_undone(self):
        with tempfile.TemporaryDirectory() as d:
            self.repo(d)
            r, tr = self.run_task(d, [
                reply(call("write_file", path=f"{d}/a.py", content="x = 2\n")),
                reply(call("write_file", path=f"{d}/new.py", content="y = 1\n")),
                reply(call("finish", summary="x is 2")),  # the check passes; the self-review rejects it
                reply(call("write_file", path=f"{d}/a.py", content="broken\n")),
                reply(call("write_file", path=f"{d}/later.py", content="z = 1\n")),
                reply(call("bash", command="true")),
                reply(call("bash", command="ls")),
                reply(call("bash", command="pwd")),
                reply(content="summary at the limit"),
            ], max_steps=8)
            self.assertEqual((r.status, r.check_passed), ("finished", True))
            self.assertTrue(r.summary.startswith("x is 2\n\nThe review round after the check did not end"))
            self.assertIn("step_limit", r.summary)
            self.assertEqual(read(d, "a.py"), "x = 2\n")
            self.assertEqual(read(d, "new.py"), "y = 1\n")
            self.assertFalse(os.path.exists(os.path.join(d, "later.py")))
            # The index is as before: the new file is untracked again.
            self.assertEqual(sorted(git(d, "status", "--short").split()), ["??", "M", "a.py", "new.py"])

    def test_a_passing_finish_after_the_review_keeps_the_new_change(self):
        with tempfile.TemporaryDirectory() as d:
            self.repo(d)
            r, tr = self.run_task(d, [
                reply(call("write_file", path=f"{d}/a.py", content="x = 2\n")),
                reply(call("finish", summary="x is 2")),
                reply(call("write_file", path=f"{d}/a.py", content="x = 2  # reviewed\n")),
                reply(call("finish", summary="x is 2, reviewed")),
            ], max_steps=10)
            self.assertEqual((r.status, r.summary), ("finished", "x is 2, reviewed"))
            self.assertEqual(read(d, "a.py"), "x = 2  # reviewed\n")


if __name__ == "__main__":
    unittest.main()
