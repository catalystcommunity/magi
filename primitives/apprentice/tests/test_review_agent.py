import json
import os
import subprocess
import tempfile
import unittest

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.config import Limits
from magi_apprentice.local import LocalExecution
from magi_apprentice.review_agent import agent_review
from magi_apprentice.tools import Tools, Workspace


def verdict(v, defects=(), met=True):
    return call("verdict", requirements=[{"text": "x", "met": met, "evidence": "a.go:1"}],
                defects=list(defects), verdict=v)


def tools_on(files):
    return Tools(Workspace(MemExecution(files), "env", "/w", Limits()))


class AgentReviewTest(unittest.TestCase):
    def test_reads_a_file_then_the_verdict_counts(self):
        m = FakeModel([reply(call("read_file", path="a.go")), reply(verdict("approve"))])
        s = agent_review(m, tools_on({"/w/a.go": "package a\n"}), "do x", "diff", max_steps=5)
        self.assertEqual(s.verdict, "approve")
        tool_msgs = [x for x in m.requests[1] if x["role"] == "tool"]
        self.assertIn("package a", tool_msgs[0]["content"])

    def test_a_blocker_makes_an_approve_revise(self):
        blocker = {"file": "a.go", "line": 3, "severity": "blocker", "problem": "said twice"}
        m = FakeModel([reply(verdict("approve", [blocker]))])
        s = agent_review(m, tools_on({}), "do x", "diff")
        self.assertEqual((s.verdict, s.defects[0]["problem"]), ("revise", "said twice"))

    def test_write_tools_are_refused_and_no_verdict_is_invalid(self):
        m = FakeModel([reply(call("write_file", path="a.go", content="x"))] * 3)
        ex = MemExecution({})
        s = agent_review(m, Tools(Workspace(ex, "env", "/w", Limits())), "do x", "diff", max_steps=3)
        self.assertEqual(s.verdict, "invalid")
        self.assertNotIn("/w/a.go", ex.files)
        self.assertIn("no tool 'write_file' in a review", m.requests[1][-1]["content"])
        # The last step offers only the verdict tool and says so.
        self.assertEqual(m.requests[2][-1]["content"], "This is the last step. Call verdict now.")

    def test_git_allows_read_commands_only(self):
        with tempfile.TemporaryDirectory() as d:
            subprocess.run(["git", "init", "-q", d], check=True)
            with open(os.path.join(d, "a.txt"), "w") as f:
                f.write("one\n")
            subprocess.run(["git", "-C", d, "add", "a.txt"], check=True)
            subprocess.run(["git", "-C", d, "-c", "user.name=t", "-c", "user.email=t@example.local",
                            "commit", "-qm", "first"], check=True)
            tools = Tools(Workspace(LocalExecution(), "env", d, Limits()))
            m = FakeModel([reply(call("git", args=["log", "--oneline"])),
                           reply(call("git", args=["commit", "-m", "x"])),
                           reply(call("git", args=["diff", "--output=/tmp/x"])),
                           reply(verdict("revise"))])
            s = agent_review(m, tools, "do x", "diff", max_steps=6)
            results = [x["content"] for x in m.requests[3] if x["role"] == "tool"]
            self.assertIn("first", results[0])
            self.assertIn("only these git commands are allowed", results[1])
            self.assertIn("that option is not allowed", results[2])
            self.assertEqual(s.verdict, "revise")

    def test_a_verdict_forced_by_the_step_limit_cannot_approve(self):
        m = FakeModel([reply(call("read_file", path="a.go")), reply(verdict("approve"))])
        s = agent_review(m, tools_on({"/w/a.go": "package a\n"}), "do x", "diff", max_steps=2)
        self.assertEqual(s.verdict, "revise")
        self.assertEqual(s.defects[-1]["problem"], "the review did not end within 2 steps")

    def test_a_model_error_is_invalid(self):
        from magi_apprentice.model import ModelError

        class Down:
            def complete(self, messages, tools):
                raise ModelError("HTTP 524", kind="unavailable")
        self.assertEqual(agent_review(Down(), tools_on({}), "do x", "diff").verdict, "invalid")


if __name__ == "__main__":
    unittest.main()
