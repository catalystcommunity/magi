"""Probes of case pr3. Target: primitives/apprentice/tests"""
import json
import unittest

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import Apprentice, Task
from magi_apprentice.config import Limits
from magi_apprentice.model import ModelError
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript

LEFT = "model calls are left"
BASH = reply(call("bash", command="true"))


class Model(FakeModel):
    """Keeps the tools of each call. A script item None raises ModelError."""

    def __init__(self, script):
        super().__init__([])
        self.items = list(script)
        self.tools = []

    def complete(self, messages, tools):
        self.requests.append(json.loads(json.dumps(messages)))
        self.tools.append(tools)
        item = self.items.pop(0) if self.items else reply(content="nothing more")
        if item is None:
            raise ModelError("HTTP 500: down")
        return item(messages) if callable(item) else item


def make(script, lim, **kw):
    model = Model(script)
    tr = Transcript(Redactor())
    return Apprentice(model, Tools(Workspace(MemExecution(), "env", "/w", lim)), lim, tr, **kw), model, tr


def notes(tr):
    return [r for r in map(json.loads, tr.records) if r["kind"] == "budget_note"]


def left(request):
    return [m["content"] for m in request if m["role"] == "user" and LEFT in m["content"]]


class Pr3(unittest.TestCase):
    def test_budget_line_after_the_work_directory_line(self):
        """Add a line "Budget: M model calls, S seconds." after the work directory line."""
        a, model, _ = make([reply(call("finish", summary="s"))], Limits(max_steps=12, max_seconds=345, self_review=False)
                           if "self_review" in Limits.__dataclass_fields__ else Limits(max_steps=12, max_seconds=345))
        a.run(Task("t"))
        lines = model.requests[0][1]["content"].split("\n")
        i = next(n for n, x in enumerate(lines) if x.startswith("Work directory:"))
        self.assertEqual(lines[i + 1].strip() or lines[i + 2].strip(), "Budget: 12 model calls, 345 seconds.")

    def test_the_two_step_notes_at_the_right_steps(self):
        """When 80% of max_steps are used, and again when 5 steps are left, add a user message before the next model call."""
        a, model, _ = make([BASH] * 10, Limits(max_steps=10))
        a.run(Task("t"))
        self.assertEqual(left(model.requests[4]), [])
        self.assertEqual(len(left(model.requests[5])), 1)
        self.assertIn("Budget: 5 of 10 model calls are left", left(model.requests[5])[0])
        self.assertEqual(len(left(model.requests[7])), 1)
        self.assertEqual(len(left(model.requests[8])), 2)
        self.assertIn("Budget: 2 of 10 model calls are left", left(model.requests[8])[1])
        self.assertEqual(len(left(model.requests[9])), 2)

    def test_the_words_of_the_note(self):
        """The note has the words of the task."""
        a, model, _ = make([BASH] * 10, Limits(max_steps=10))
        a.run(Task("t"))
        self.assertIn("Finish the work that is necessary, run the check, and call finish with a summary. "
                      "Do not start new improvements.", left(model.requests[5])[0])
        self.assertRegex(left(model.requests[5])[0], r"\(and \d+ seconds of the time limit\)")

    def test_each_note_is_in_the_transcript(self):
        """Record each note in the transcript (kind "budget_note")."""
        a, _, tr = make([BASH] * 10, Limits(max_steps=10))
        a.run(Task("t"))
        self.assertEqual(len(notes(tr)), 2)

    def test_the_time_note_goes_in_once(self):
        """Do the same for max_seconds at 80% (one note), with the clock that Apprentice takes."""
        now = [0]

        def late(_messages):
            now[0] = 850
            return BASH
        a, model, tr = make([late, BASH, BASH, reply(call("finish", summary="s"))],
                            Limits(max_seconds=1000), clock=lambda: now[0])
        a.run(Task("t"))
        self.assertEqual(left(model.requests[0]), [])
        self.assertEqual(len(left(model.requests[1])), 1)
        self.assertEqual(len(left(model.requests[3])), 1)
        self.assertEqual(len(notes(tr)), 1)

    def test_no_notes_in_a_short_task(self):
        """No notes in a short task."""
        a, model, tr = make([BASH, reply(call("finish", summary="s"))], Limits())
        a.run(Task("t"))
        self.assertEqual(notes(tr), [])
        self.assertEqual([left(r) for r in model.requests], [[]] * len(model.requests))

    def test_a_second_run_gets_its_notes(self):
        """Each note goes in once in each run (review lesson rl-0001)."""
        a, _, tr = make([BASH] * 22, Limits(max_steps=10))
        a.run(Task("t"))
        a.run(Task("t"))
        self.assertEqual(len(notes(tr)), 4)

    def test_the_last_call_at_the_step_limit(self):
        """Make one more model call with no tools. Put that text in Result.summary, and keep the status."""
        a, model, _ = make([BASH] * 3 + [reply(content="I changed a.py.")], Limits(max_steps=3))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary, r.steps), ("step_limit", "I changed a.py.", 3))
        self.assertEqual(len(model.requests), 4)
        self.assertFalse(model.tools[3])
        self.assertEqual(model.requests[3][-1]["role"], "user")

    def test_the_last_call_at_the_time_limit(self):
        """The same when the time limit stops the loop."""
        now = [0]

        def late(_messages):
            now[0] = 2000
            return BASH
        a, model, _ = make([late, reply(content="I changed a.py.")], Limits(max_seconds=1000),
                           clock=lambda: now[0])
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary), ("time_limit", "I changed a.py."))
        self.assertFalse(model.tools[-1])

    def test_a_stop_at_the_time_limit_is_not_an_error(self):
        """Keep the status step_limit or time_limit: the result gets no error text."""
        now = [0]

        def late(_messages):
            now[0] = 2000
            return BASH
        a, _, _ = make([late, reply(content="I changed a.py.")], Limits(max_seconds=1000), clock=lambda: now[0])
        self.assertIsNone(a.run(Task("t")).error)

    def test_the_last_call_is_counted_and_recorded(self):
        """The last call is a model call: its tokens are counted and the transcript has its answer."""
        a, _, tr = make([BASH] * 3 + [reply(content="I changed a.py.")], Limits(max_steps=3))
        r = a.run(Task("t"))
        self.assertEqual(r.usage["prompt_tokens"], 400)
        kinds = [x["kind"] for x in map(json.loads, tr.records) if "I changed a.py." in json.dumps(x)]
        self.assertGreater(len([k for k in kinds if k != "result"]), 0)

    def test_a_failed_last_call_keeps_the_summary(self):
        """If that call fails, keep the current summary text."""
        a, _, _ = make([BASH] * 3 + [None], Limits(max_steps=3))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary), ("step_limit", "Stopped after 3 model calls (step limit)."))

    def test_no_last_call_after_finish(self):
        """The last call is only for a stop at a limit."""
        a, model, _ = make([BASH, reply(call("finish", summary="s"))],
                           Limits(self_review=False) if "self_review" in Limits.__dataclass_fields__ else Limits())
        r = a.run(Task("t"))
        self.assertEqual((r.status, len(model.requests)), ("finished", 2))
