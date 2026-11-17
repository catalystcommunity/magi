import json
import unittest

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import Apprentice, Task
from magi_apprentice.config import Limits
from magi_apprentice.model import ModelError
from magi_apprentice.supervisor import Supervisor, parse_decision
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript


def decision(verdict: str, message: str = "do y", category: str = "looping") -> str:
    return json.dumps({"verdict": verdict, "category": category, "message": message})


class ParseTest(unittest.TestCase):
    def test_parse(self):
        d = parse_decision("Thinking...\n" + decision("steer", "stop the grep; edit a.go"))
        self.assertEqual((d.verdict, d.category, d.message), ("steer", "looping", "stop the grep; edit a.go"))
        self.assertEqual(parse_decision("no json").verdict, "escalate")
        self.assertEqual(parse_decision('{"verdict": "pause"}').verdict, "escalate")
        self.assertEqual(parse_decision(decision("steer", "")).verdict, "escalate")
        self.assertEqual(parse_decision(decision("continue", "")).verdict, "continue")


class DueTest(unittest.TestCase):
    def test_triggers_checkpoints_and_the_gap(self):
        s = Supervisor(FakeModel([]), max_steps=100)
        self.assertEqual(s.due(3, "guard: repeat"), "guard: repeat")
        s.last_step = 3
        self.assertIsNone(s.due(5, "guard: repeat"))  # within 5 steps of the last call
        self.assertIsNone(s.due(10, None))
        self.assertEqual(s.due(25, None), "checkpoint: 25% of the step budget is used")
        s.last_step = 25
        self.assertIsNone(s.due(40, None))  # the 25% checkpoint passed once
        self.assertEqual(s.due(50, None), "checkpoint: 50% of the step budget is used")

    def test_a_supervisor_that_is_down_says_continue(self):
        class Down:
            def complete(self, messages, tools):
                raise ModelError("HTTP 524", kind="unavailable")
        d = Supervisor(Down(), 10).ask(1, "t", "guard: repeat", [], "")
        self.assertEqual(d.verdict, "continue")


def run(worker_script, supervisor_answers, max_steps=40):
    lim = Limits(max_steps=max_steps, self_review=False)
    sup_model = FakeModel([reply(content=a) for a in supervisor_answers])
    tr = Transcript(Redactor())
    worker = FakeModel(worker_script)
    r = Apprentice(worker, Tools(Workspace(MemExecution(), "env", "/w", lim)), lim, tr,
                   supervisor=Supervisor(sup_model, max_steps)).run(Task("fix a.go"))
    sups = [x for x in map(json.loads, tr.records) if x["kind"] == "supervisor"]
    return r, worker, sup_model, sups


class InTheLoopTest(unittest.TestCase):
    def test_a_repeat_note_asks_the_supervisor_and_its_steer_reaches_the_worker(self):
        script = [reply(call("bash", command="grep -rn x ."))] * 3 + [reply(call("finish", summary="done"))]
        r, worker, sup, sups = run(script, [decision("steer", "Stop the grep. Read a.go and edit it.")])
        self.assertEqual(r.status, "finished")
        self.assertEqual([(s["trigger"], s["verdict"]) for s in sups], [("guard: repeat", "steer")])
        self.assertIn("Supervisor note: Stop the grep. Read a.go and edit it.", worker.requests[3][-1]["content"])
        # The supervisor saw the task and the repeated call.
        prompt = sup.requests[0][1]["content"]
        self.assertIn("fix a.go", prompt)
        self.assertIn("grep -rn x .", prompt)

    def test_stop_and_escalate_end_the_run_as_blocked_statuses(self):
        script = [reply(call("bash", command="grep -rn x ."))] * 5
        r, _, _, _ = run(script, [decision("stop", "no network for cargo", "blocked_environment")])
        self.assertEqual(r.status, "supervisor_stop")
        r, _, _, _ = run(script, ["not json"])
        self.assertEqual(r.status, "escalated")

    def test_checkpoints_ask_without_a_trigger(self):
        script = [reply(call("bash", command=f"step {i}")) for i in range(12)] + [reply(call("finish", summary="done"))]
        r, _, _, sups = run(script, [decision("continue", "", "progressing")] * 2, max_steps=20)
        self.assertEqual(r.status, "finished")
        self.assertEqual([s["trigger"] for s in sups],
                         ["checkpoint: 25% of the step budget is used", "checkpoint: 50% of the step budget is used"])


if __name__ == "__main__":
    unittest.main()
