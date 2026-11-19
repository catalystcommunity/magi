import json
import os
import tempfile
import unittest
from unittest import mock

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import Task
from magi_apprentice.config import Limits, ModelConfig
from magi_apprentice.model import ModelError
from magi_apprentice.runner import run_task
from magi_apprentice.spec import PROMPT, acceptance_checks


class AcceptanceChecksTest(unittest.TestCase):
    def test_the_model_reads_only_the_instructions_and_its_heading_goes(self):
        model = FakeModel([reply(content="Acceptance checks:\n\n1. \"Return 3.\" Result: f() == 3.")])
        checks, rec = acceptance_checks(model, "  Make f return 3.  ")
        self.assertEqual(checks, "1. \"Return 3.\" Result: f() == 3.")
        self.assertEqual(model.requests, [[{"role": "system", "content": PROMPT},
                                           {"role": "user", "content": "Task:\n\nMake f return 3."}]])
        self.assertEqual(rec["chars"], len(checks))
        self.assertNotIn("error", rec)

    def test_a_failed_call_gives_no_checks(self):
        def fail(messages):
            raise ModelError("HTTP 503")
        checks, rec = acceptance_checks(FakeModel([fail]), "Make f return 3.")
        self.assertEqual(checks, "")
        self.assertEqual(rec["error"], "ModelError: HTTP 503")


class RunTaskSpecStepTest(unittest.TestCase):
    """run_task puts the checks of the review model at the end of the instructions."""

    def run_task(self, instructions, review_model="qwen-review", limits=None, checks="1. \"Return 3.\""):
        seen = []

        def fake_checks(model, text):
            seen.append((model.cfg.name, model.cfg.temperature, text))
            return checks, {"chars": len(checks), "seconds": 0.1}

        worker = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        path = os.path.join(tmp.name, "t.jsonl")
        with mock.patch("magi_apprentice.runner.acceptance_checks", fake_checks), \
                mock.patch("magi_apprentice.runner.pre_review",
                           lambda *a, **k: {"verdict": "approve", "blockers": [], "seconds": 0.1}):
            run_task(Task(instructions), execution=MemExecution(), environment="env_1",
                           model_cfg=ModelConfig(name="worker", api_key="k-abcdef"),
                           limits=limits or Limits(), model=worker, review_model=review_model,
                           transcript_path=path)
        with open(path) as f:
            self.records = [json.loads(line) for line in f]
        return worker, seen

    def test_the_worker_reads_the_checks_after_the_instructions(self):
        worker, seen = self.run_task("Make f return 3.")
        self.assertEqual(seen, [("qwen-review", 0.3, "Make f return 3.")])
        spec = [r for r in self.records if r["kind"] == "spec_step"]
        self.assertEqual([(r["model"], r["chars"]) for r in spec], [("qwen-review", 14)])
        self.assertIn("Task instructions:\nMake f return 3.\n\nAcceptance checks:\n\n1. \"Return 3.\"",
                      worker.requests[0][1]["content"])

    def test_no_spec_step_without_a_review_model_or_when_it_is_off(self):
        for kw in ({"review_model": None}, {"limits": Limits(spec_step=False)}):
            worker, seen = self.run_task("Make f return 3.", **kw)
            self.assertEqual(seen, [], kw)
            self.assertNotIn("Acceptance checks", worker.requests[0][1]["content"])

    def test_checks_that_the_task_has_stay_alone(self):
        worker, seen = self.run_task("Make f return 3.\n\nAcceptance checks:\n\n1. mine")
        self.assertEqual(seen, [])
        self.assertEqual(worker.requests[0][1]["content"].count("Acceptance checks"), 1)

    def test_no_checks_leave_the_instructions_as_they_are(self):
        worker, seen = self.run_task("Make f return 3.", checks="")
        self.assertEqual(len(seen), 1)
        self.assertNotIn("Acceptance checks", worker.requests[0][1]["content"])


if __name__ == "__main__":
    unittest.main()
