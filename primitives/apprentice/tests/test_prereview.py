import json
import os
import subprocess
import tempfile
import unittest

from fakes import FakeModel, reply

from magi_apprentice.config import Limits
from magi_apprentice.local import LocalExecution
from magi_apprentice.prereview import pre_review, summary
from magi_apprentice.review import LENSES
from magi_apprentice.tools import Tools, Workspace


def answer(defects: list, verdict: str) -> str:
    return "```json\n" + json.dumps({"requirements": [{"text": "x", "met": True, "evidence": "a.go:3"}],
                                     "defects": defects, "verdict": verdict}) + "\n```"


def repo(d: str) -> str:
    def git(*args):
        return subprocess.run(["git", "-C", d, "-c", "user.name=t", "-c", "user.email=t@example.local", *args],
                              check=True, capture_output=True, text=True).stdout.strip()
    with open(os.path.join(d, "a.go"), "w") as f:
        f.write("package a\n\nfunc A() int {\n\treturn 1\n}\n")
    git("init", "-q")
    git("add", "-A")
    git("commit", "-qm", "base")
    base = git("rev-parse", "HEAD")
    with open(os.path.join(d, "a.go"), "w") as f:
        f.write("package a\n\nfunc A() int {\n\treturn 2\n}\n")
    git("commit", "-qam", "change")
    return base


class PreReviewTest(unittest.TestCase):
    def test_the_change_since_base_with_checks_and_only_real_blockers(self):
        with tempfile.TemporaryDirectory() as d:
            base = repo(d)
            compile_claim = {"file": "a.go", "line": 4, "severity": "blocker", "problem": "undefined: B"}
            real = {"file": "a.go", "line": 4, "severity": "blocker", "problem": "A returns 2; the task says 3."}
            m = FakeModel([reply(content=answer([], "approve")), reply(content=answer([compile_claim], "revise"))]
                          + [reply(content=answer([], "approve"))] * 2 + [reply(content=answer([real], "revise"))])
            out = pre_review(m, Tools(Workspace(LocalExecution(), "l", d, Limits())), "make A return 3", base)
            self.assertEqual(len(m.requests), len(LENSES))
            user = m.requests[0][1]["content"]
            self.assertIn("+\treturn 2", user)
            self.assertEqual(out["verdict"], "revise")
            self.assertEqual(out["blockers"], [{"lens": "scope", "file": "a.go", "line": 4,
                                                "problem": "A returns 2; the task says 3."}])
            self.assertEqual(summary(out), "The model review found 1 possible blocker(s). Check them:\n"
                                           "- [scope] a.go:4 A returns 2; the task says 3.")

    def test_no_blocker_approves(self):
        with tempfile.TemporaryDirectory() as d:
            base = repo(d)
            m = FakeModel([reply(content=answer([], "approve"))] * len(LENSES))
            out = pre_review(m, Tools(Workspace(LocalExecution(), "l", d, Limits())), "make A return 2", base)
            self.assertEqual((out["verdict"], out["blockers"]), ("approve", []))
            self.assertEqual(summary(out), "The model review found no blocker.")

    def test_a_failed_review_says_so(self):
        self.assertEqual(summary({"verdict": "revise", "blockers": [], "error": "timeout"}),
                         "The model review failed: timeout")


if __name__ == "__main__":
    unittest.main()
