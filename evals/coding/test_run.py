"""Tests of evals/coding/run.py with a fake client: no network.

The fake client records the calls and answers with task views that the
code-change workflow of Magi produces.
"""
import json
import os
import sys
import tempfile
import unittest
import unittest.mock
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[2].joinpath("clients", "python")))

import run as R  # noqa: E402
from magi_rpc import RpcError  # noqa: E402
from magi_magi import types as T  # noqa: E402


def step(step_id, status, notes=(), outcome=None):
    return T.TaskStep(step=step_id, status=status, notes=list(notes), started_ms=1, outcome=outcome)


def task(status, steps, current_step="", error=None):
    return T.Task(id="run_1", kind="code-change", title="t", status=status, current_step=current_step,
                  steps=steps, refs=[], created_ms=1, updated_ms=2, error=error)


WORK_NOTES = [
    "The apprentice started.",
    "apprentice finished after 42 steps, 87 tool calls (cost $2.647): the change is done",
]
WORK_NOTES_TWICE = WORK_NOTES + [
    "apprentice finished after 12 steps, 20 tool calls: the checks failed, so I tried again",
]


class TestWorkTotals(unittest.TestCase):
    def test_cost_and_steps_of_one_work_step(self):
        got = R.work_totals(task("waiting", [step("work", "done", WORK_NOTES)]))
        self.assertEqual(got, {"status": "finished", "steps": 42, "tool_calls": 87, "cost": 2.647})

    def test_no_cost_is_none(self):
        got = R.work_totals(task("waiting", [
            step("work", "done", ["apprentice finished after 3 steps, 4 tool calls: done"])]))
        self.assertEqual(got, {"status": "finished", "steps": 3, "tool_calls": 4, "cost": None})

    def test_two_work_steps_add_up(self):
        """A task that the check step sent back has two work steps."""
        got = R.work_totals(task("waiting", [step("work", "done", WORK_NOTES_TWICE)]))
        self.assertEqual(got["steps"], 54)
        self.assertEqual(got["tool_calls"], 107)
        self.assertEqual(got["cost"], 2.647)

    def test_other_notes_do_not_count(self):
        got = R.work_totals(task("waiting", [
            step("prepare", "done", ["Environment env_1 is ready on branch task/run_1."]),
            step("work", "done", WORK_NOTES),
            step("check", "done", ["Checks passed. Waiting for review."], outcome="pass"),
        ]))
        self.assertEqual(got["steps"], 42)


class TestCheckView(unittest.TestCase):
    def test_pass_with_attempts(self):
        got = R.check_view(task("waiting", [
            step("check", "done", ["Checks failed (attempt 1)."], outcome="fail-checks"),
            step("check", "done", ["Checks passed."], outcome="pass"),
        ]))
        self.assertEqual(got, {"outcome": "pass", "attempts": 2, "step_outcome": "pass"})

    def test_fail(self):
        got = R.check_view(task("gave-up", [
            step("check", "done", ["The checks failed 3 times."], outcome="give-up"),
        ]))
        self.assertEqual(got["outcome"], "fail")
        self.assertEqual(got["attempts"], 1)

    def test_no_check_step(self):
        self.assertIsNone(R.check_view(task("failed", [step("prepare", "failed", ["no runner"])])))


class TestAtEnd(unittest.TestCase):
    def test_running_is_not_the_end(self):
        self.assertFalse(R.at_end(task("running", [step("work", "claimed")], "work")))

    def test_waiting_is_the_end(self):
        self.assertTrue(R.at_end(task("waiting", [step("review", "waiting")], "review")))

    def test_ended_run_is_the_end(self):
        for status in ("succeeded", "failed", "cancelled"):
            self.assertTrue(R.at_end(task(status, [])), status)


class TestCall401(unittest.TestCase):
    def test_401_raises_token_expired(self):
        class C:
            def get_task(self, req):
                raise RpcError(401, "no caller")

        with self.assertRaises(R.TokenExpired):
            R.call(C(), "get_task", T.GetTaskRequest(id="run_1"))

    def test_other_errors_pass_through(self):
        class C:
            def get_task(self, req):
                raise RpcError(404, "no run run_1")

        with self.assertRaises(RpcError):
            R.call(C(), "get_task", T.GetTaskRequest(id="run_1"))


class TestPoll(unittest.TestCase):
    def poll(self, tasks, errors=(), max_minutes=10):
        """A client that answers with tasks in turn, then errors. The clock
        and the sleep are fake, so the poll loop runs without a delay."""
        calls = {"n": 0, "sleeps": 0}

        class C:
            def get_task(self, req):
                i = calls["n"]
                calls["n"] += 1
                if i < len(tasks):
                    return tasks[i]
                raise RpcError(500, "boom")

        def sleep(s):
            calls["sleeps"] += 1

        t, out = R.poll(C(), "run_1", max_minutes, sleep=sleep, now=lambda: 0.0)
        return t, out, calls

    def test_ends_when_the_task_waits(self):
        t, out, calls = self.poll([task("running", [step("work", "claimed")], "work"),
                                   task("waiting", [step("review", "waiting")], "review")])
        self.assertEqual(t.status, "waiting")
        self.assertFalse(out)
        self.assertEqual(calls["n"], 2)

    def test_ends_when_the_run_is_over(self):
        t, out, _ = self.poll([task("running", [], "check"), task("gave-up", [])])
        self.assertEqual(t.status, "gave-up")
        self.assertFalse(out)

    def test_time_limit_returns_the_last_view(self):
        t, out, calls = self.poll([task("running", [], "work")] * 3, max_minutes=0)
        self.assertEqual(t.status, "running")
        self.assertTrue(out)
        self.assertEqual(calls["sleeps"], 0)

    def test_time_limit_with_no_answer_returns_none(self):
        """No poll answered before the time ran out."""
        calls = {"n": 0}

        class C:
            def get_task(self, req):
                calls["n"] += 1
                raise RpcError(500, "boom")

        t, out = R.poll(C(), "run_1", 0, sleep=lambda s: None, now=lambda: 0.0)
        self.assertIsNone(t)
        self.assertTrue(out)
        self.assertEqual(calls["n"], 1)

    def test_five_errors_in_a_row_stop_the_case(self):
        """The fifth error of a row stops the case, after four sleeps."""
        calls = {"n": 0, "sleeps": 0}

        class C:
            def get_task(self, req):
                calls["n"] += 1
                raise RpcError(500, "boom")

        def sleep(s):
            calls["sleeps"] += 1

        with self.assertRaises(RpcError):
            R.poll(C(), "run_1", 10, sleep=sleep, now=lambda: 0.0)
        self.assertEqual(calls["n"], 5)
        self.assertEqual(calls["sleeps"], 4)

    def test_an_error_then_a_task_keeps_going(self):
        """An error of one poll is logged and the poll repeats."""
        calls = {"n": 0}

        class C:
            def get_task(self, req):
                calls["n"] += 1
                if calls["n"] == 1:
                    raise RpcError(500, "boom")
                return task("waiting", [step("review", "waiting")], "review")

        t, out = R.poll(C(), "run_1", 10, sleep=lambda s: None, now=lambda: 0.0)
        self.assertEqual(t.status, "waiting")
        self.assertEqual(calls["n"], 2)


class TestMain(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.out = os.path.join(self.dir.name, "results")
        self.addCleanup(self.dir.cleanup)

    def run_main(self, client, cases="pr2", minutes="1"):
        argv = ["--url", "http://example.local", "--token-file", "/dev/null", "--model",
                '{"name": "m1", "context_tokens": 60000}', "--label", "cand", "--cases", cases,
                "--max-minutes", minutes, "--out", self.out]
        R.main(argv, client, sleep=lambda s: None)

    def test_one_case_writes_result_and_diff(self):
        class C:
            def __init__(self):
                self.started = []

            def start_task(self, req):
                self.started.append(req)
                return task("running", [], "prepare")

            def get_task(self, req):
                return task("waiting", [
                    step("prepare", "done", ["Environment env_1 is ready."]),
                    step("work", "done", WORK_NOTES),
                    step("check", "done", ["Checks passed."], outcome="pass"),
                    step("review", "waiting"),
                ], "review")

            def get_task_diff(self, req):
                return T.TaskDiff(task=req.id, patch="--- a/f\n+++ b/f\n", truncated=False,
                                  files=[T.FileChange(path="f", additions=1, deletions=0)])

            def get_task_output(self, req):
                self.output = req
                return T.TaskOutput(task=req.id, name=req.name, media_type="text/plain",
                                    text="ALL CHECKS PASSED\n", truncated=False)

        c = C()
        self.run_main(c)
        req = c.started[0]
        self.assertEqual(req.kind, "code-change")
        self.assertEqual(req.repo, "magi")
        self.assertEqual(req.title, "Apprentice: project instructions and repository picture at start")
        self.assertEqual(req.base, "c347c04a923a3ad3c435265bcd3345d5572fb552")
        self.assertEqual(json.loads(req.model), {"name": "m1", "context_tokens": 60000})
        self.assertIn("Project instructions", req.instructions)
        d = os.path.join(self.out, "cand", "pr2")
        with open(os.path.join(d, "result.json")) as f:
            r = json.load(f)
        self.assertEqual(r["id"], "pr2")
        self.assertEqual(r["label"], "cand")
        self.assertEqual(r["model"], {"name": "m1", "context_tokens": 60000})
        self.assertEqual(r["run"], "run_1")
        self.assertEqual(r["status"], "waiting")
        self.assertEqual(r["step"], "review")
        self.assertEqual(r["check"], {"outcome": "pass", "attempts": 1, "step_outcome": "pass"})
        self.assertEqual(r["apprentice"], {"status": "finished", "steps": 42, "tool_calls": 87, "cost": 2.647})
        self.assertEqual(r["minutes"], 0.0)
        self.assertIn("f", r["files"])
        self.assertIn("primitives/apprentice/README.md", r["files"])  # the changed files of the case
        with open(os.path.join(d, "diff.patch")) as f:
            self.assertEqual(f.read(), "--- a/f\n+++ b/f\n")
        # The check output comes from the same operation as `magi output`.
        self.assertEqual(c.output.name, "check.output")
        with open(os.path.join(d, "check.output.txt")) as f:
            self.assertEqual(f.read(), "ALL CHECKS PASSED\n")

    def test_401_stops_the_run_with_a_message(self):
        """The script stops with a clear message when the token expired."""
        class C:
            def start_task(self, req):
                return task("running", [], "prepare")

            def get_task(self, req):
                raise RpcError(401, "no caller")

        with self.assertRaises(R.TokenExpired):
            self.run_main(C())
        self.assertFalse(os.path.exists(os.path.join(self.out, "cand")))
        # cli() turns the 401 into the message that the operator reads.
        with unittest.mock.patch.object(R, "main", side_effect=R.TokenExpired("no caller")):
            with self.assertRaises(SystemExit) as e:
                R.cli()
        self.assertIn("token expired", str(e.exception))
        self.assertIn("no caller", str(e.exception))

    def test_unknown_case_id(self):
        with self.assertRaises(SystemExit):
            self.run_main(None, cases="nope")

    def test_time_limit_writes_a_result(self):
        """A case that does not end in time still gets a result file."""
        class C:
            def start_task(self, req):
                return task("running", [], "work")

            def get_task(self, req):
                return task("running", [step("work", "claimed")], "work")

            def get_task_diff(self, req):
                raise RpcError(409, "the task has no checked change yet")

            def get_task_output(self, req):
                raise RpcError(404, "task run_1 has no check.output yet")

        self.run_main(C(), minutes="0")
        with open(os.path.join(self.out, "cand", "pr2", "result.json")) as f:
            r = json.load(f)
        self.assertEqual(r["status"], "running")
        self.assertEqual(r["step"], "work")
        self.assertIn("time limit", r["error"])
        self.assertEqual(r["diff_files"], [])
        with open(os.path.join(os.path.dirname(__file__), "cases.jsonl")) as f:
            changed = json.loads(f.readline())["changed"]
        self.assertEqual(r["files"], sorted(changed))


if __name__ == "__main__":
    unittest.main()
