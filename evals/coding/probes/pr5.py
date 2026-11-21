"""Probes of case pr5. Target: primitives/apprentice/tests"""
import unittest

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import SYSTEM_PROMPT, Apprentice, Task
from magi_apprentice.config import Limits
from magi_apprentice.model import Reply
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript

RULE = ("Each model call costs money. When tool calls do not depend on each other, make them in one reply: "
        "for example, read three files, or read a file and search, in one reply. "
        "Read the files that the task names before you search.")


def answer(*calls, content="", **usage):
    return Reply(content=content, tool_calls=list(calls), finish_reason="tool_calls", usage=usage)


def run(script, **limits):
    lim = Limits(self_review=False, **limits)
    a = Apprentice(FakeModel(script), Tools(Workspace(MemExecution(), "env", "/w", lim)), lim, Transcript(Redactor()))
    return a.run(Task("t"))


class Pr5(unittest.TestCase):
    def test_the_rule_is_after_the_rule_about_the_tools(self):
        """Add one rule after the rule "Use the tools to inspect..."."""
        text = " ".join(SYSTEM_PROMPT.split())
        self.assertIn(RULE, text)
        before = text[:text.index(RULE)].rstrip()
        self.assertTrue(before.endswith("read or search first. -"), before[-80:])

    def test_the_cost_of_two_answers(self):
        """Two answers with cost 0.01 and 0.02 give usage["cost"] 0.03 and the cached tokens."""
        r = run([answer(call("bash", command="true"), prompt_tokens=100, completion_tokens=10, cost=0.01,
                        prompt_tokens_details={"cached_tokens": 40}),
                 answer(call("finish", summary="s"), prompt_tokens=200, completion_tokens=20, cost=0.02,
                        prompt_tokens_details={"cached_tokens": 60})])
        self.assertIsInstance(r.usage["cost"], float)
        self.assertAlmostEqual(r.usage["cost"], 0.03)
        self.assertEqual((r.usage["cached_tokens"], r.usage["prompt_tokens"], r.usage["completion_tokens"]),
                         (100, 300, 30))

    def test_a_missing_field_counts_as_0(self):
        """A missing field counts as 0."""
        r = run([answer(call("bash", command="true"), prompt_tokens=100, completion_tokens=10),
                 answer(call("finish", summary="s"), prompt_tokens=200, completion_tokens=20, cost=0.02,
                        prompt_tokens_details={})])
        self.assertAlmostEqual(r.usage["cost"], 0.02)
        self.assertEqual(r.usage["cached_tokens"], 0)

    def test_usage_has_the_fields_with_no_cost(self):
        """usage["cost"] and usage["cached_tokens"] are next to the token counts, also when they are 0."""
        r = run([answer(call("finish", summary="s"), prompt_tokens=100, completion_tokens=10)])
        self.assertEqual(r.usage, {"prompt_tokens": 100, "completion_tokens": 10, "cost": 0.0, "cached_tokens": 0})

    def test_a_null_details_field_counts_as_0(self):
        """A provider can send null for prompt_tokens_details; that counts as 0."""
        r = run([answer(call("finish", summary="s"), prompt_tokens=100, completion_tokens=10, cost=0.01,
                        prompt_tokens_details=None)])
        self.assertEqual((r.status, r.usage["cached_tokens"]), ("finished", 0))

    def test_the_cost_of_the_last_call_at_a_limit(self):
        """Add the cost of each answer (also of the last call at a limit)."""
        r = run([answer(call("bash", command="true"), prompt_tokens=100, cost=0.01),
                 answer(content="I changed a.py.", prompt_tokens=100, cost=0.04,
                        prompt_tokens_details={"cached_tokens": 7})], max_steps=1)
        self.assertEqual(r.status, "step_limit")
        self.assertAlmostEqual(r.usage["cost"], 0.05)
        self.assertEqual(r.usage["cached_tokens"], 7)
