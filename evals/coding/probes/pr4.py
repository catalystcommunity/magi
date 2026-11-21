"""Probes of case pr4. Target: primitives/apprentice/tests"""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import Apprentice, Task
from magi_apprentice.config import Limits
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript

TEXT = ("Review your change before you finish, as a strict reviewer of this repository. Check each point and "
        "fix what is wrong: (1) Each part of the task instructions is done; (2) the project instructions are "
        "followed (generated files, tests, documentation style); (3) no change that the task did not ask for "
        "(renames, reformatting, unrelated edits); (4) no debug output, temporary files, or commented-out code; "
        "(5) tests for each new behavior, and they test the behavior, not the implementation; (6) names and "
        "comments are clear, and comments tell why. Then call finish again with the summary. If you change "
        "files, run the check again first.")
DIFF = " x.py | 2 +-\n--- a/x.py\n+++ b/x.py\n@@ -1 +1 @@\n-x = 1\n+x = 2222\n"
BASH = reply(call("bash", command="true"))


def fin(s):
    return reply(call("finish", summary=s))


class Exec(MemExecution):
    """Answers a git diff command with a diff, and fails the check when told."""

    def __init__(self, diff=DIFF, diff_code=0, check_codes=()):
        super().__init__()
        self.diff, self.diff_code, self.check_codes = diff, diff_code, list(check_codes)
        self.diffs = 0

    def exec(self, req):
        r = super().exec(req)
        script = req.argv[-1]
        if "./check.sh" in script:
            r.exit_code = self.check_codes.pop(0) if self.check_codes else 0
        elif "git diff" in script:
            self.diffs += 1
            r.exit_code = self.diff_code
            r.stdout = b"" if self.diff_code else self.diff.encode()
        return r


def make(script, lim=None, e=None, **kw):
    lim = lim or Limits()
    e = e or Exec()
    model = FakeModel(script)
    tr = Transcript(Redactor())
    return Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr, **kw), model, e, tr


def words(text):
    return " ".join(text.split())


class Pr4(unittest.TestCase):
    def test_the_second_finish_ends_the_task(self):
        """The second finish call ends the task as now."""
        a, _, _, _ = make([fin("one"), fin("two"), fin("three")])
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary, r.steps), ("finished", "two", 2))

    def test_one_exec_for_the_diff(self):
        """Get git diff --stat and git diff of the work directory through the same Tools object (one exec)."""
        a, _, e, _ = make([fin("one"), fin("two")])
        a.run(Task("t"))
        self.assertEqual(e.diffs, 1)
        self.assertEqual(len([x for x in e.execs if "git diff --stat" in x.argv[-1]]), 1)

    def test_the_review_is_once_in_each_run(self):
        """The review happens once in each run."""
        a, model, e, _ = make([fin("one"), BASH, fin("two"), fin("three"), fin("four")])
        r1 = a.run(Task("t"))
        r2 = a.run(Task("t"))
        self.assertEqual((r1.summary, r2.summary, e.diffs), ("two", "four", 2))

    def test_the_check_gate_applies_to_the_second_finish(self):
        """The check gate still applies to it."""
        a, model, _, _ = make([fin("one"), fin("two"), fin("three")], e=Exec(check_codes=[0, 1, 0]))
        r = a.run(Task("t", check_command="./check.sh"))
        self.assertEqual((r.status, r.summary, r.check_passed), ("finished", "three", True))

    def test_no_review_after_a_failed_check(self):
        """When the model calls finish for the first time and the check passed: a failed check gives no review."""
        a, model, e, _ = make([fin("one"), fin("two"), fin("three")], e=Exec(check_codes=[1, 0, 0]))
        r = a.run(Task("t", check_command="./check.sh"))
        self.assertNotIn("Review your change", model.requests[1][-1]["content"])
        self.assertIn("Review your change", model.requests[2][-1]["content"])
        self.assertEqual(r.summary, "three")

    def test_self_review_false_ends_at_the_first_finish(self):
        """self_review=False ends at the first finish."""
        a, _, e, _ = make([fin("one"), fin("two")], Limits(self_review=False))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary, e.diffs), ("finished", "one", 0))

    def test_the_environment_turns_it_off(self):
        """Environment MAGI_APPRENTICE_SELF_REVIEW, "false" turns it off."""
        self.assertTrue(Limits().self_review)
        old = os.environ.get("MAGI_APPRENTICE_SELF_REVIEW")
        os.environ["MAGI_APPRENTICE_SELF_REVIEW"] = "false"
        try:
            self.assertFalse(Limits.from_env().self_review)
        finally:
            os.environ.pop("MAGI_APPRENTICE_SELF_REVIEW")
            if old is not None:
                os.environ["MAGI_APPRENTICE_SELF_REVIEW"] = old

    def test_no_review_when_fewer_than_5_calls_are_left(self):
        """The review does not happen when fewer than 5 model calls are left."""
        a, _, e, _ = make([BASH, BASH, fin("one"), fin("two")], Limits(max_steps=7))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary, e.diffs), ("finished", "one", 0))

    def test_a_review_when_5_calls_are_left(self):
        """Fewer than 5: with 5 calls left the review happens."""
        a, _, e, _ = make([BASH, BASH, fin("one"), fin("two")], Limits(max_steps=8))
        r = a.run(Task("t"))
        self.assertEqual((r.summary, e.diffs), ("two", 1))

    def test_no_review_when_80_percent_of_the_time_is_used(self):
        """The review does not happen when 80% of the time is used."""
        now = [0]

        def late(_messages):
            now[0] = 850
            return fin("one")
        a, _, e, _ = make([late, fin("two")], Limits(max_seconds=1000), clock=lambda: now[0])
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary, e.diffs), ("finished", "one", 0))

    def test_a_failed_diff_command_still_gives_the_text(self):
        """A failed diff command still gives the review text (with "(no diff)")."""
        a, model, _, _ = make([fin("one"), fin("two")], e=Exec(diff_code=1))
        r = a.run(Task("t"))
        content = model.requests[1][-1]["content"]
        self.assertIn("(no diff)", content)
        self.assertIn(TEXT, words(content))
        self.assertEqual(r.summary, "two")


class Pr4Git(unittest.TestCase):
    """The review against a real git repository."""

    def setUp(self):
        from magi_apprentice.local import LocalExecution
        self.local = LocalExecution()
        self.dir = tempfile.TemporaryDirectory()
        self.d = self.dir.name
        env = {k: v for k, v in os.environ.items() if "GIT_" not in k}
        Path(self.d, "x.py").write_text("x = 1\n")
        for args in (["init", "-q"], ["add", "-A"],
                     ["-c", "user.email=a@b", "-c", "user.name=t", "commit", "-qm", "first"]):
            subprocess.run(["git", *args], cwd=self.d, check=True, env=env)

    def tearDown(self):
        self.dir.cleanup()

    def review(self, body, lim=None):
        Path(self.d, "x.py").write_text(body)
        lim = lim or Limits()
        model = FakeModel([fin("one"), fin("two")])
        tr = Transcript(Redactor())
        a = Apprentice(model, Tools(Workspace(self.local, "local", self.d, lim)), lim, tr)
        a.run(Task("t", workdir=self.d))
        return model.requests[1][-1], tr

    def test_the_first_finish_is_rejected_with_the_diff_and_the_text(self):
        """Reject that finish with a tool result that has the diff and this text."""
        result, _ = self.review("x = 2222\n")
        self.assertEqual(result["role"], "tool")
        self.assertIn("+x = 2222", result["content"])
        self.assertIn("1 file changed", result["content"])
        self.assertIn(TEXT, words(result["content"]))

    def test_the_diff_is_cut_to_tool_output_chars(self):
        """Cut the diff to tool_output_chars."""
        result, _ = self.review("line of the file\n" * 2000, Limits(tool_output_chars=3000))
        self.assertLess(len(result["content"]), 3000 + len(TEXT) + 600)
        self.assertIn("+line of the file", result["content"])
        self.assertIn(TEXT, words(result["content"]))

    def test_the_review_is_in_the_transcript(self):
        """Record the review in the transcript (kind "self_review", with the diff size in characters)."""
        _, tr = self.review("x = 2222\n")
        recs = [r for r in map(json.loads, tr.records) if r["kind"] == "self_review"]
        self.assertEqual(len(recs), 1)
        sizes = [v for k, v in recs[0].items() if isinstance(v, int) and k not in ("ts", "at_ms", "time", "step")]
        self.assertTrue([v for v in sizes if 80 <= v <= 1000], recs[0])
