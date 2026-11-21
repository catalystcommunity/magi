import unittest
from unittest import mock

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import Task
from magi_apprentice.config import Limits, ModelConfig
from magi_apprentice.runner import run_task


class RunTaskModelReviewTest(unittest.TestCase):
    """run_task gives the apprentice the lens review when review_model is set."""

    def run_task(self, review_model):
        calls = []

        def fake_pre_review(model, tools, task, base, lessons="", samples=1):
            calls.append((model.cfg.name, model.cfg.max_tokens, task, base, lessons, samples))
            return {"verdict": "revise", "seconds": 1.0,
                    "blockers": [{"lens": "tests", "file": "a_test.go", "line": 9, "problem": "No test of B."}]}
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        task = Task("make A return 3", title="Return 3", review_comment="Add a test.", lessons="- a lesson")
        with mock.patch("magi_apprentice.runner.pre_review", fake_pre_review):
            out = run_task(task, execution=MemExecution(), environment="env_1",
                           model_cfg=ModelConfig(name="worker", api_key="k-abcdef"), limits=Limits(),
                           model=model, review_model=review_model)
        return out, model, calls

    def test_the_review_model_reviews_the_task_with_its_comment(self):
        out, model, calls = self.run_task("qwen-review")
        self.assertEqual(out.result.summary, "b")
        self.assertEqual(calls, [("qwen-review", 12000,
                                  "Return 3\n\nmake A return 3\n\nThe review comment of the earlier round "
                                  "(each item must be done):\nAdd a test.", "HEAD", "- a lesson", 2)])
        self.assertIn("- [tests] a_test.go:9 No test of B.", model.requests[1][-1]["content"])

    def test_the_review_writes_status_lines(self):
        # One review is many model calls and no step of the apprentice.
        notes = []

        def fake_pre_review(model, tools, task, base, lessons="", samples=1):
            model.alive("a model call of the review", now=True)
            return {"verdict": "approve", "seconds": 1.0, "blockers": []}
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        with mock.patch("magi_apprentice.runner.pre_review", fake_pre_review):
            run_task(Task("make A return 3"), execution=MemExecution(), environment="env_1",
                     model_cfg=ModelConfig(name="worker", api_key="k-abcdef"), limits=Limits(spec_step=False),
                     model=model, review_model="qwen-review", on_progress=notes.append)
        self.assertIn("working: the review of the change", notes)
        self.assertIn("working: a model call of the review", notes)

    def test_no_review_model_keeps_the_checklist(self):
        out, model, calls = self.run_task(None)
        self.assertEqual(calls, [])
        self.assertIn("review your change first", model.requests[1][-1]["content"])


if __name__ == "__main__":
    unittest.main()
