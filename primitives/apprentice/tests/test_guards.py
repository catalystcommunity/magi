import json
import unittest

from fakes import FakeModel, MemExecution, _res, call, reply

from magi_apprentice.apprentice import Apprentice, Task
from magi_apprentice.config import Limits
from magi_apprentice.guards import Guards, error_signature, outcome_of, round_comment
from magi_apprentice.model import ModelError
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript


def bash(cmd: str) -> str:
    return json.dumps({"command": cmd})


class FailingExec(MemExecution):
    """Every command fails with stderr. A {n} in it is the call number, so
    that two attempts differ only in a number."""

    def __init__(self, stderr: str):
        super().__init__()
        self.stderr = stderr

    def exec(self, req):
        self.execs.append(req)
        return _res(1, b"", self.stderr.format(n=len(self.execs)).encode())


def run(script, exec_=None, limits=None, task=None):
    lim = limits or Limits(max_steps=60, self_review=False)
    model = FakeModel(script)
    tr = Transcript(Redactor())
    tools = Tools(Workspace(exec_ or MemExecution(), "env", "/w", lim))
    r = Apprentice(model, tools, lim, tr).run(task or Task("t"))
    return r, model, [x for x in map(json.loads, tr.records) if x["kind"] == "guard"]


class GuardsTest(unittest.TestCase):
    def test_the_same_call_and_result_note_at_3_stop_at_5(self):
        g = Guards()
        got = [g.tool("bash", bash("grep -rn x ."), "exit_code: 0\nsame", True) for _ in range(5)]
        self.assertEqual([a and (a.note is not None, a.stop) for a in got],
                         [None, None, (True, None), None, (False, "loop")])
        self.assertIn("You ran the same command 3 times", got[2].note)

    def test_a_different_result_is_not_a_repeat(self):
        g = Guards()
        got = [g.tool("bash", bash("make"), f"exit_code: 0\nbuild {i}", True) for i in range(6)]
        self.assertEqual(got, [None] * 6)

    def test_old_calls_leave_the_window(self):
        # 2 repeats, 9 other calls, 2 repeats: never 3 in the last 10.
        g = Guards()
        calls = [("ls", "a")] * 2 + [(f"cmd {i}", "b") for i in range(9)] + [("ls", "a")] * 2
        got = [g.tool("bash", bash(c), r, True) for c, r in calls]
        self.assertEqual(got, [None] * 13)

    def test_the_same_error_from_different_commands(self):
        g = Guards()
        err = "exit_code: 101\nerror: failed to compile `x v{n}`, at /tmp/build-{n}/x\n"
        got = [g.tool("bash", bash(f"cargo build -j{i}"), err.format(n=i), False) for i in range(5)]
        self.assertEqual([a and (a.reason, a.stop) for a in got],
                         [None, None, ("error repeat", None), None,
                          ("the same error from 5 different commands", "loop")])
        self.assertIn("error: failed to compile `x v#`, at #", got[2].note)

    def test_error_signature_removes_numbers_and_temp_paths(self):
        a = error_signature("exit_code: 1\nerror: failed at /tmp/x-123/y line 45\nok line")
        b = error_signature("exit_code: 1\nerror: failed at /tmp/x-999/y line 7\nok line")
        self.assertEqual(a, "error: failed at # line #")
        self.assertEqual(a, b)
        self.assertIsNone(error_signature("exit_code: 1\nall good\n"))

    def test_an_environment_error_gets_one_note(self):
        g = Guards()
        out = "exit_code: 101\nerror: download of config.json failed (Connection reset by peer)"
        first = g.tool("bash", bash("cargo test"), out, False)
        self.assertEqual(first.reason, "environment")
        self.assertIn("Do not try to fix the environment", first.note)
        self.assertIn("Connection reset by peer", first.note)
        # The same pattern again: no second note (the error rule counts it).
        self.assertIsNone(g.tool("bash", bash("cargo build"), out, False))

    def test_no_edit_note_at_40_stop_at_120_and_an_edit_resets(self):
        g = Guards()
        got = [g.step() for _ in range(40)]
        self.assertEqual([i for i, a in enumerate(got) if a], [39])
        self.assertIn("You changed no file in 40 steps", got[39].note)
        g.tool("edit_file", json.dumps({"path": "a"}), "edited a", True)
        got = [g.step() for _ in range(120)]
        self.assertEqual([(i, a.stop) for i, a in enumerate(got) if a], [(39, None), (119, "no_progress")])

    def test_a_revise_round_has_a_shorter_no_edit_limit(self):
        g = Guards(revise=True)
        got = [g.step() for _ in range(75)]
        self.assertEqual([(i, a.stop) for i, a in enumerate(got) if a], [(24, None), (74, "no_progress")])

    def test_thinking_cuts_count_over_the_whole_run(self):
        g = Guards()
        self.assertEqual([g.thinking_cut() for _ in range(5)], [None] * 5)
        self.assertEqual(g.thinking_cut().stop, "stalled")


class GuardsInTheLoopTest(unittest.TestCase):
    def test_a_repeated_command_ends_the_run_with_loop(self):
        r, model, guards = run([reply(call("bash", command="grep -rn x ."))] * 20)
        self.assertEqual(r.status, "loop")
        # 5 model calls with the command, then the last call for the summary.
        self.assertEqual((r.steps, len(model.requests)), (5, 6))
        self.assertEqual([(g["rule"], g["stop"]) for g in guards],
                         [("repeat", None), ("the same command ran 5 times with the same result", "loop")])
        # The note is in the request after the third result.
        self.assertIn("You ran the same command 3 times", model.requests[3][-1]["content"])

    def test_an_environment_error_note_reaches_the_model(self):
        ex = FailingExec("error: download of config.json failed (Connection reset by peer)")
        r, model, guards = run([reply(call("bash", command="cargo test"))] + [reply(call("finish", summary="blocked"))],
                               exec_=ex)
        self.assertEqual(r.status, "finished")
        self.assertEqual([g["rule"] for g in guards], ["environment"])
        self.assertIn("This error comes from the environment", model.requests[1][-1]["content"])

    def test_six_thinking_cuts_in_separate_streaks_end_the_run(self):
        cut = reply(finish_reason="length", reasoning="x" * 100)
        script = []
        for i in range(5):
            script += [cut, reply(call("bash", command=f"step {i}"))]
        r, _, guards = run(script + [cut])
        self.assertEqual((r.status, r.steps), ("stalled", 11))
        self.assertEqual(guards[-1]["stop"], "stalled")

    def test_a_budget_error_is_blocked_provider(self):
        class Broke:
            requests = 0

            def complete(self, messages, tools):
                Broke.requests += 1
                raise ModelError("HTTP 429: BudgetLimitError", kind="budget")
        lim = Limits(max_steps=10, self_review=False)
        r = Apprentice(Broke(), Tools(Workspace(MemExecution(), "env", "/w", lim)), lim,
                       Transcript(Redactor())).run(Task("t"))
        self.assertEqual((r.status, r.error, Broke.requests), ("blocked_provider", "HTTP 429: BudgetLimitError", 1))

    def test_an_unavailable_endpoint_is_model_unavailable(self):
        class Down:
            def complete(self, messages, tools):
                raise ModelError("model request failed after 6 attempts: HTTP 524", kind="unavailable")
        lim = Limits(max_steps=10, self_review=False)
        r = Apprentice(Down(), Tools(Workspace(MemExecution(), "env", "/w", lim)), lim,
                       Transcript(Redactor())).run(Task("t"))
        self.assertEqual(r.status, "model_unavailable")


class OutcomeTest(unittest.TestCase):
    def test_each_status_has_its_outcome(self):
        got = {s: outcome_of(s) for s in ("finished", "step_limit", "time_limit", "error", "stopped", "loop",
                                         "no_progress", "stalled", "blocked_provider", "model_unavailable")}
        self.assertEqual(got, {"finished": "done", "step_limit": "done", "time_limit": "done", "error": "fail",
                               "stopped": "retry", "loop": "blocked", "no_progress": "blocked",
                               "stalled": "blocked", "blocked_provider": "blocked", "model_unavailable": "blocked"})

    def test_the_comment_of_the_next_round(self):
        v = {"review.comment": "fix the test", "blocked.comment": "use the cache"}
        self.assertEqual(round_comment({**v, "apprentice.blocked": ""}), "fix the test")
        self.assertEqual(round_comment({**v, "apprentice.blocked": "true"}), "use the cache")
        self.assertIsNone(round_comment({"apprentice.blocked": "true", "review.comment": "old"}))
        self.assertIsNone(round_comment({}))


if __name__ == "__main__":
    unittest.main()
