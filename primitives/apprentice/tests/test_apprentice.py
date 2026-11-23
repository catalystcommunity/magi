import io
import json
import os
import unittest
import urllib.error

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import SELF_REVIEW, Apprentice, Context, Task, model_wait_recorder
from magi_apprentice.config import Limits, ModelConfig
from magi_apprentice.model import ChatModel, ModelError
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript

SECRET = "sk-very-secret-987654"

# A good Chat Completions answer for a model client test: one finish call.
OK_REPLY = {"choices": [{"finish_reason": "tool_calls", "message": {"role": "assistant", "content": "", "tool_calls": [
    {"id": "x1", "type": "function", "function": {"name": "finish", "arguments": "{\"summary\": \"done\"}"}}]}}]}


def distinct_calls(n: int) -> list:
    """n replies with a different bash command each, so that the repeat
    guard does not end the run."""
    return [reply(call("bash", command=f"true {i}")) for i in range(n)]


def apprentice(script, files=None, limits=None, **kw):
    lim = limits or Limits()
    m = MemExecution(files)
    model = FakeModel(script)
    tr = Transcript(Redactor([SECRET]))
    return Apprentice(model, Tools(Workspace(m, "env", "/work/repo", lim)), lim, tr, **kw), model, m, tr


def kinds(tr):
    return [json.loads(line)["kind"] for line in tr.records]


def records(tr, kind):
    return [json.loads(line) for line in tr.records if json.loads(line)["kind"] == kind]


DIFF = ("--- git diff --stat ---\n 1 file changed, 1 insertion(+)\n"
        "--- git diff ---\n--- a/x.py\n+++ b/x.py\n@@ -1 +1 @@\n-x = 1\n+x = 2\n")


def diff_exec():
    """An execution client that answers the diff script with DIFF."""
    class Exec(MemExecution):
        def exec(self, req):
            r = super().exec(req)
            if "git diff --stat" in req.argv[-1]:
                r.stdout = DIFF.encode()
            return r
    return Exec()


class LoopTest(unittest.TestCase):
    def test_write_then_finish(self):
        a, model, m, tr = apprentice([reply(call("write_file", path="x.py", content="x = 1\n")),
                                 reply(call("finish", summary="wrote x.py"))],
                                 limits=Limits(self_review=False))
        r = a.run(Task("make x.py"))
        self.assertEqual((r.status, r.summary, r.steps, r.tool_calls), ("finished", "wrote x.py", 2, 2))
        self.assertEqual(m.files["/work/repo/x.py"], b"x = 1\n")
        # the second request carries the tool result paired to the call id
        last = model.requests[1]
        self.assertEqual(last[-1]["role"], "tool")
        self.assertEqual(last[-1]["tool_call_id"], last[-2]["tool_calls"][0]["id"])
        self.assertNotIn("_name", last[-1])
        self.assertEqual(kinds(tr)[0], "task")
        self.assertIn("tool_result", kinds(tr))
        self.assertEqual(kinds(tr)[-1], "result")

    def test_prompt_includes_check_output_and_review(self):
        a, model, _, _ = apprentice([reply(call("finish", summary="s"))], limits=Limits(self_review=False))
        a.run(Task("fix it", title="Fix", check_output="FAIL: test_x", review_comment="rename y"))
        user = model.requests[0][1]["content"]
        self.assertIn("The previous attempt failed these checks:\nFAIL: test_x", user)
        self.assertIn("The reviewer asked for these changes:\nrename y", user)
        self.assertIn("Title: Fix", user)

    def test_check_gate_rejects_failed_check(self):
        results = iter([1, 0])

        class Exec(MemExecution):
            def exec(self, req):
                r = super().exec(req)
                if req.argv[-1] == "./check.sh":
                    r.exit_code = next(results)
                return r
        lim = Limits(self_review=False)
        e = Exec()
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(Task("t", check_command="./check.sh"))
        self.assertEqual((r.status, r.summary, r.check_passed), ("finished", "b", True))
        self.assertIn("finish rejected", model.requests[1][-1]["content"])

    def test_check_gate_gives_up_after_retries(self):
        class Exec(MemExecution):
            def exec(self, req):
                r = super().exec(req)
                r.exit_code = 1
                return r
        lim = Limits(check_retries=2, self_review=False)
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        r = Apprentice(model, Tools(Workspace(Exec(), "env", "/w", lim)), lim, Transcript(Redactor())).run(
            Task("t", check_command="false"))
        self.assertEqual((r.status, r.check_passed), ("finished", False))

    def test_stub_summary_is_rejected(self):
        # Compaction can leave a finish call with a stub summary. The model then
        # sends that stub as the summary of its next finish call.
        stub = "[compacted 1978 chars]"
        model = FakeModel([reply(call("finish", summary=stub)),
                           reply(call("finish", summary="I changed a.py and added a test."))])
        lim = Limits(self_review=False)
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(MemExecution(), "env", "/w", lim)), lim, tr).run(
            Task("t", check_command="./check.sh"))
        self.assertEqual((r.status, r.summary, r.steps),
                         ("finished", "I changed a.py and added a test.", 2))
        result = model.requests[1][-1]["content"]
        self.assertIn("finish rejected: this summary is a compaction stub", result)
        self.assertIn("Write the full summary", result)
        # the stub is not a failed check: the check runs only for the second finish
        self.assertEqual(len(records(tr, "check")), 1)
        self.assertEqual(r.check_passed, True)

    def test_stub_summary_does_not_use_up_the_review(self):
        stub = "[compacted 1978 chars]"
        e = diff_exec()
        model = FakeModel([reply(call("finish", summary=stub)),
                           reply(call("finish", summary="I changed a.py.")),
                           reply(call("finish", summary="I changed a.py. Reviewed."))])
        lim = Limits()
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(Task("t"))
        self.assertEqual((r.status, r.summary), ("finished", "I changed a.py. Reviewed."))
        self.assertIn("compaction stub", model.requests[1][-1]["content"])
        self.assertIn("review your change first", model.requests[2][-1]["content"])
        self.assertEqual(len(records(tr, "self_review")), 1)

    def test_stalls_without_tool_calls(self):
        a, model, _, _ = apprentice([reply(content="thinking")] * 3)
        r = a.run(Task("t"))
        self.assertEqual(r.status, "stalled")
        self.assertIn("call finish", model.requests[1][-1]["content"])

    def test_thinking_cut_gets_its_own_note(self):
        # A reply that the limit cut during thinking has no content and no tool
        # call. The advice for a cut with content does not fit this case.
        a, model, _, tr = apprentice([reply(content="", finish_reason="length", reasoning="plan " * 300),
                                      reply(call("finish", summary="s"))],
                                     limits=Limits(self_review=False))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary), ("finished", "s"))
        self.assertIn("cut at the output limit while you were still thinking",
                      model.requests[1][-1]["content"])
        self.assertIn("call one tool now", model.requests[1][-1]["content"])
        self.assertNotIn("Write large files in parts", model.requests[1][-1]["content"])
        # the reasoning does not go back to the model
        self.assertNotIn("plan plan", json.dumps(model.requests[1]))
        cuts = records(tr, "thinking_cut")
        self.assertEqual((cuts[0]["step"], cuts[0]["reasoning_chars"]), (1, 1500))
        resp = records(tr, "model_response")[0]
        self.assertEqual(resp["reasoning_chars"], 1500)
        # 1500 chars is above the 800 limit, so the record keeps the head and
        # the tail, not the whole text
        self.assertEqual(resp["reasoning_head"], "plan " * 80)
        self.assertEqual(resp["reasoning_tail"], "plan " * 80)
        self.assertNotIn("plan " * 81, json.dumps(resp))

    def test_a_cut_with_content_keeps_the_old_note(self):
        a, model, _, tr = apprentice([reply(content="half a file", finish_reason="length"),
                                      reply(call("finish", summary="s"))],
                                     limits=Limits(self_review=False))
        a.run(Task("t"))
        self.assertIn("Write large files in parts", model.requests[1][-1]["content"])
        self.assertNotIn("still thinking", model.requests[1][-1]["content"])
        self.assertEqual(records(tr, "thinking_cut"), [])

    def test_the_transcript_keeps_the_head_and_the_tail_of_long_reasoning(self):
        a, _, _, tr = apprentice([reply(content="", finish_reason="length", reasoning="r" * 2000),
                                  reply(call("finish", summary="s"))],
                                 limits=Limits(self_review=False))
        a.run(Task("t"))
        resp = records(tr, "model_response")[0]
        self.assertEqual(resp["reasoning_chars"], 2000)
        self.assertEqual(resp["reasoning_head"], "r" * 400)
        self.assertEqual(resp["reasoning_tail"], "r" * 400)
        # the record stays small: it holds the head and the tail, not the whole
        # reasoning (2000 chars alone would make it longer than 2000)
        self.assertLess(len(json.dumps(resp)), 1500)
        self.assertNotIn("r" * 401, json.dumps(resp))

    def test_a_thinking_cut_compacts_a_long_context(self):
        # A long context makes the long thinking more likely, so a thinking cut
        # compacts at 60% of the threshold, not at 100%.
        big = "z" * 20000
        a, model, _, tr = apprentice([reply(call("bash", command="x")),
                                      reply(content="", finish_reason="length", reasoning="r" * 100),
                                      reply(call("finish", summary="s"))],
                                     limits=Limits(context_tokens=3000, tool_output_chars=8000,
                                                   self_review=False))

        class Exec(MemExecution):
            def exec(self, req):
                r = super().exec(req)
                r.stdout = big.encode()
                return r
        a.tools.ws.client = Exec()
        a.run(Task("t"))
        compactions = records(tr, "compaction")
        self.assertEqual(compactions[-1]["reason"], "thinking cut")
        self.assertLess(compactions[-1]["est_tokens"], 3000)
        self.assertGreater(compactions[-1]["removed_chars"], 0)
        # the next request is smaller than the one before the cut
        self.assertLess(sum(len(json.dumps(m)) for m in model.requests[2]),
                        sum(len(json.dumps(m)) for m in model.requests[1]))

    def test_no_compaction_after_a_thinking_cut_below_60_percent(self):
        a, _, _, tr = apprentice([reply(content="", finish_reason="length", reasoning="r"),
                                  reply(call("finish", summary="s"))],
                                 limits=Limits(self_review=False))
        a.run(Task("t"))
        self.assertEqual(records(tr, "compaction"), [])

    def test_the_stall_summary_names_the_thinking_cuts(self):
        a, _, _, tr = apprentice([reply(content="", finish_reason="length", reasoning="r")] * 3)
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary), ("stalled", "The model's replies were cut while it was thinking (3 times)"))
        self.assertEqual(len(records(tr, "thinking_cut")), 3)
        # a stall without thinking cuts keeps the old summary
        a, _, _, _ = apprentice([reply(content="thinking")] * 3)
        self.assertEqual(a.run(Task("t")).summary, "thinking")

    def test_the_count_of_thinking_cuts_is_the_state_of_one_run(self):
        # The same object runs a second time: the count starts at 0 again, so
        # the second summary does not name the cuts of the first run.
        a, _, _, tr = apprentice([reply(content="", finish_reason="length", reasoning="r")] * 3
                                 + [reply(content="thinking")] * 3)
        self.assertEqual(a.run(Task("t")).summary,
                         "The model's replies were cut while it was thinking (3 times)")
        self.assertEqual(a.run(Task("t")).summary, "thinking")
        self.assertEqual(len(records(tr, "thinking_cut")), 3)

    def test_a_tool_call_resets_the_count_of_thinking_cuts(self):
        # The stall summary counts the replies of one streak, so a run that
        # recovered between cuts does not say that it stalled on thinking.
        script = ([reply(content="", finish_reason="length", reasoning="r"),
                   reply(call("bash", command="true"))] * 2
                  + [reply(content="thinking")] * 3)
        a, _, _, tr = apprentice(script)
        r = a.run(Task("t"))
        self.assertEqual(r.status, "stalled")
        self.assertEqual(r.summary, "thinking")
        self.assertEqual(len(records(tr, "thinking_cut")), 2)

    def test_step_limit(self):
        a, _, _, _ = apprentice([reply(call("bash", command="true"))] * 10, limits=Limits(max_steps=3))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.steps), ("step_limit", 3))

    def test_time_limit(self):
        t = iter(range(0, 10000, 1000))
        a, _, _, _ = apprentice([reply(call("bash", command="true"))] * 10,
                           limits=Limits(max_seconds=2500), clock=lambda: next(t))
        self.assertEqual(a.run(Task("t")).status, "time_limit")

    def test_should_stop(self):
        a, _, _, _ = apprentice([reply(call("bash", command="true"))], should_stop=lambda: "cancelled")
        self.assertEqual(a.run(Task("t")).status, "stopped")

    def test_steering_notes_reach_the_model(self):
        notes = [["use Go, not Python"], []]
        a, model, _, tr = apprentice([reply(call("finish", summary="s"))], steering=lambda: notes.pop(0),
                                     limits=Limits(self_review=False))
        a.run(Task("t"))
        self.assertIn("Operator steering note: use Go, not Python", model.requests[0][-1]["content"])
        self.assertIn("steering", kinds(tr))

    def test_progress_every_n_tool_calls(self):
        notes = []
        a, _, _, _ = apprentice([reply(call("bash", command="echo 1"), call("bash", command="echo 2")),
                            reply(call("finish", summary="s"))],
                           limits=Limits(progress_every=2, self_review=False), on_progress=notes.append)
        a.run(Task("t"))
        self.assertEqual(len(notes), 1)
        self.assertIn("bash echo 1; bash echo 2", notes[0])

    def test_a_quiet_run_writes_working_lines(self):
        # The check and the review write no step line. The stall check of
        # Magi stops a task with no status line, so these phases write one.
        notes = []
        t = iter(range(0, 100000, 200))
        a, _, _, _ = apprentice([reply(call("bash", command="echo 1")), reply(call("finish", summary="s"))],
                                limits=Limits(progress_every=50, self_review=False, alive_every_s=180),
                                on_progress=notes.append, clock=lambda: next(t))
        r = a.run(Task("t", check_command="true"))
        self.assertEqual(r.status, "finished")
        self.assertIn("working: the check command", notes)
        self.assertTrue(any(n.startswith("working: a model call") or n.startswith("working: the tool") for n in notes), notes)
        self.assertFalse(any(n.startswith("step ") for n in notes))

    def test_a_run_with_step_lines_writes_no_working_line(self):
        notes = []
        a, _, _, _ = apprentice([reply(call("bash", command="echo 1"), call("bash", command="echo 2")),
                                 reply(call("finish", summary="s"))],
                                limits=Limits(progress_every=2, self_review=False), on_progress=notes.append)
        a.run(Task("t"))
        self.assertEqual([n for n in notes if n.startswith("working")], [])

    def test_model_error_is_reported(self):
        class Broken:
            def complete(self, m, t):
                raise ModelError("HTTP 401: bad key")
        lim = Limits()
        r = Apprentice(Broken(), Tools(Workspace(MemExecution(), "e", "/w", lim)), lim, Transcript(Redactor())).run(Task("t"))
        self.assertEqual(r.status, "error")
        self.assertIn("401", r.error)

    def test_a_wait_for_a_busy_host_is_in_the_transcript(self):
        # The model client tells the loop about each wait, so the transcript
        # shows why a model call took long.
        class BusyOnce:
            """Answers one busy 503, then a good answer."""
            def __init__(self):
                self.busy = True

            def __call__(self, req, timeout):
                if self.busy:
                    self.busy = False
                    body = json.dumps({"error": {"type": "host_busy", "message": "stopped"}}).encode()
                    raise urllib.error.HTTPError("u", 503, "err", {"Retry-After": "60"}, io.BytesIO(body))
                return io.BytesIO(json.dumps(OK_REPLY).encode())

        lim = Limits(self_review=False)
        tr = Transcript(Redactor())
        cfg = ModelConfig(base_url="http://m.example.local/v1", name="m", busy_wait_s=600)
        model = ChatModel(cfg, sleep=lambda s: None, opener=BusyOnce(), on_wait=model_wait_recorder(tr))
        r = Apprentice(model, Tools(Workspace(MemExecution(), "e", "/w", lim)), lim, tr).run(Task("t"))
        self.assertEqual(r.status, "finished")
        waits = records(tr, "model_wait")
        self.assertEqual(len(waits), 1)
        self.assertEqual((waits[0]["seconds"], waits[0]["reason"]), (60.0, "host_busy"))

    def test_usage_sums_cost_and_cached_tokens(self):
        # The provider gives the cost in dollars and the cached prompt tokens
        # inside prompt_tokens_details. A missing field counts as 0.
        a, _, _, _ = apprentice(
            [reply(call("write_file", path="x", content="1"),
                   usage={"prompt_tokens": 100, "completion_tokens": 10, "cost": 0.01,
                          "prompt_tokens_details": {"cached_tokens": 40}}),
             reply(call("finish", summary="s"),
                   usage={"prompt_tokens": 200, "cost": 0.02, "prompt_tokens_details": {}})],
            limits=Limits(self_review=False))
        r = a.run(Task("t"))
        self.assertEqual(r.usage["cost"], 0.03)
        self.assertEqual(r.usage["cached_tokens"], 40)
        self.assertEqual(r.usage["prompt_tokens"], 300)
        self.assertEqual(r.usage["completion_tokens"], 10)

    def test_usage_without_cost_or_details(self):
        a, _, _, _ = apprentice([reply(call("finish", summary="s"))], limits=Limits(self_review=False))
        r = a.run(Task("t"))
        self.assertEqual(r.usage, {"prompt_tokens": 100, "completion_tokens": 10,
                                   "cached_tokens": 0, "cost": 0.0})


class BudgetTest(unittest.TestCase):
    """The budget notes and the last call at a limit."""

    def notes(self, tr):
        return [json.loads(l) for l in tr.records if json.loads(l)["kind"] == "budget_note"]

    def test_two_step_notes_at_the_right_steps(self):
        # With 10 steps, 5 are left after step 5 and 80% are used at step 8.
        # Each note goes in once, before the next model call.
        script = distinct_calls(10)
        a, model, _, tr = apprentice(script, limits=Limits(max_steps=10))
        a.run(Task("t"))
        notes = self.notes(tr)
        self.assertEqual(len(notes), 2)
        self.assertIn("Budget: 5 of 10 model calls are left", notes[0]["note"])
        self.assertIn("Budget: 2 of 10 model calls are left", notes[1]["note"])
        # the first note is in the request of step 6, the second in step 9
        self.assertIn("Budget: 5 of 10", model.requests[5][-1]["content"])
        self.assertIn("Budget: 2 of 10", model.requests[8][-1]["content"])
        self.assertNotIn("model calls are left", model.requests[4][-1]["content"])

    def test_two_runs_each_get_the_step_note(self):
        # The note flags are state of one run, not of the object.
        a, model, _, tr = apprentice(distinct_calls(20),
                                     limits=Limits(max_steps=10))
        for _ in range(2):
            a.run(Task("t"))
        self.assertEqual(len(self.notes(tr)), 4)
        # the first run has 10 steps and one last call, so the second starts at 12
        self.assertIn("Budget: 5 of 10", model.requests[5][-1]["content"])
        self.assertIn("Budget: 5 of 10", model.requests[16][-1]["content"])

    def test_time_note_uses_the_apprentice_clock(self):
        # Clock values: the start, then the limit check and the note check of
        # each loop turn. The fifth value puts the task at 80% of 1000 s, so
        # the note goes in before the second model call.
        t = iter([0, 0, 0, 0, 800, 800, 800])
        a, model, _, tr = apprentice([reply(call("bash", command="true")),
                                      reply(call("finish", summary="s"))],
                                     limits=Limits(max_seconds=1000), clock=lambda: next(t))
        a.run(Task("t"))
        notes = self.notes(tr)
        self.assertEqual(len(notes), 1)
        self.assertIn("Budget: 59 of 60 model calls are left (and 200 seconds", notes[0]["note"])
        self.assertIn("Budget: 59 of 60", model.requests[1][-1]["content"])
        self.assertNotIn("model calls are left", model.requests[0][-1]["content"])

    def test_last_call_at_the_step_limit(self):
        script = [reply(call("bash", command="true"))] * 3 + [
            reply(content="I changed a.py.", usage={"prompt_tokens": 100, "cost": 0.04})]
        a, model, _, tr = apprentice(script, limits=Limits(max_steps=3))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary, r.steps), ("step_limit", "I changed a.py.", 3))
        # the last call has no tools and asks for the summary
        self.assertEqual(model.requests[-1][-1]["role"], "user")
        self.assertIn("finish summary", model.requests[-1][-1]["content"])
        self.assertIn("last_call", kinds(tr))
        self.assertEqual(kinds(tr)[-1], "result")
        # the last call is not a step, but its tokens and cost are counted
        self.assertEqual(r.usage["prompt_tokens"], 400)
        self.assertEqual(r.usage["cost"], 0.04)
        self.assertEqual(len(model.requests), 4)

    def test_failed_last_call_keeps_the_summary(self):
        class Broken(FakeModel):
            def complete(self, messages, tools):
                if not tools:
                    raise ModelError("HTTP 500: down")
                return super().complete(messages, tools)
        model = Broken([reply(call("bash", command="true"))] * 3)
        lim = Limits(max_steps=3)
        a = Apprentice(model, Tools(Workspace(MemExecution(), "env", "/w", lim)), lim,
                       Transcript(Redactor()))
        r = a.run(Task("t"))
        self.assertEqual(r.status, "step_limit")
        self.assertEqual(r.summary, "Stopped after 3 model calls (step limit).")

    def test_last_call_written_as_a_finish_call(self):
        # The last call has no tools, but the parser makes a tool call of a
        # finish that the model wrote as text. Its summary is the answer.
        script = [reply(call("bash", command="true"))] * 3 + [reply(call("finish", summary="I changed a.py."))]
        a, _, _, _ = apprentice(script, limits=Limits(max_steps=3))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.summary), ("step_limit", "I changed a.py."))

    def test_two_step_limits_at_one_step_give_one_note(self):
        # With 25 steps, 80% used and 5 left are the same step.
        a, model, _, tr = apprentice([reply(call("bash", command=f"echo {i}")) for i in range(21)]
                                     + [reply(call("finish", summary="s"))],
                                     limits=Limits(max_steps=25, self_review=False))
        a.run(Task("t"))
        self.assertEqual(len(self.notes(tr)), 1)
        self.assertIn("Budget: 5 of 25 model calls are left", self.notes(tr)[0]["note"])

    def test_no_notes_in_a_short_task(self):
        a, model, _, tr = apprentice([reply(call("write_file", path="x", content="1")),
                                      reply(call("finish", summary="s"))],
                                     limits=Limits(self_review=False))
        r = a.run(Task("t"))
        self.assertEqual(r.status, "finished")
        self.assertEqual(self.notes(tr), [])
        # the budget line in the first message is not a note
        self.assertIn("Budget: 60 model calls, 1800 seconds.", model.requests[0][1]["content"])
        self.assertNotIn("model calls are left", model.requests[0][1]["content"])
        self.assertNotIn("last_call", kinds(tr))

    def test_budget_line_in_the_first_message(self):
        a, model, _, _ = apprentice([reply(call("finish", summary="s"))],
                                    limits=Limits(max_steps=12, max_seconds=345, self_review=False))
        a.run(Task("t"))
        user = model.requests[0][1]["content"]
        self.assertIn("Budget: 12 model calls, 345 seconds.", user)
        self.assertLess(user.index("Work directory:"), user.index("Budget: 12 model calls"))


class SelfReviewTest(unittest.TestCase):
    """The first finish that passes the check gate gets one review request."""

    def reviewed(self, tr):
        return records(tr, "self_review")

    def test_first_finish_is_rejected_with_the_diff(self):
        e = diff_exec()
        model = FakeModel([reply(call("finish", summary="done")),
                           reply(call("finish", summary="done, reviewed"))])
        lim = Limits()
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(Task("t"))
        self.assertEqual((r.status, r.summary, r.steps), ("finished", "done, reviewed", 2))
        result = model.requests[1][-1]["content"]
        self.assertIn("finish rejected: review your change first.", result)
        self.assertIn("git diff --stat\n 1 file changed, 1 insertion(+)", result)
        self.assertIn("git diff\n--- a/x.py\n+++ b/x.py", result)
        self.assertIn("Review your change before you finish", result)
        self.assertIn("(6) names and comments are clear", result)
        self.assertIn("call finish again with the summary", result)
        # one exec gives the stat and the diff
        self.assertEqual(len([x for x in e.execs if "git diff --stat" in x.argv[-1]]), 1)
        # the transcript records the size of the diff that the model saw
        head = "finish rejected: review your change first.\n"
        diff = result[len(head):-len("\n\n" + SELF_REVIEW)]
        self.assertEqual(self.reviewed(tr)[0]["diff_chars"], len(diff))
        # the second finish is accepted, and the review does not repeat
        self.assertEqual(len(model.requests), 2)
        self.assertEqual(kinds(tr)[-1], "result")
        self.assertEqual(len(self.reviewed(tr)), 1)

    def test_diff_script_puts_new_files_in_the_diff(self):
        # git diff does not show untracked files, so the script puts them in
        # the index as empty entries before it runs git diff.
        e = diff_exec()
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        lim = Limits()
        tr = Transcript(Redactor())
        Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(Task("t"))
        script = next(x.argv[-1] for x in e.execs if "git diff --stat" in x.argv[-1])
        self.assertIn("git add --intent-to-add --all", script)
        self.assertLess(script.index("git add --intent-to-add --all"),
                        script.index("git diff --stat"))

    def test_diff_script_restores_the_index(self):
        # The intent-to-add entries stay in the index after git diff, and an
        # index that is not empty breaks git stash. The script resets the index
        # at the end, after the diff commands.
        e = diff_exec()
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        lim = Limits()
        tr = Transcript(Redactor())
        Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(Task("t"))
        script = next(x.argv[-1] for x in e.execs if "git diff --stat" in x.argv[-1])
        self.assertIn("git reset -q", script)
        self.assertGreater(script.index("git reset -q"), script.index("git diff --stat"))
        self.assertGreater(script.index("git reset -q"), script.rindex("git diff"))

    def test_review_after_a_failed_check(self):
        # The check gate rejects the first finish, so the review waits for the
        # finish that passes it.
        codes = iter([1, 0, 0])

        class Exec(MemExecution):
            def exec(self, req):
                r = super().exec(req)
                if req.argv[-1] == "./check.sh":
                    r.exit_code = next(codes)
                return r
        e = Exec()
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b")),
                           reply(call("finish", summary="c"))])
        lim = Limits()
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(
            Task("t", check_command="./check.sh"))
        self.assertEqual((r.status, r.summary, r.check_passed), ("finished", "c", True))
        self.assertIn("the check command failed", model.requests[1][-1]["content"])
        self.assertIn("review your change first", model.requests[2][-1]["content"])
        self.assertEqual(len(self.reviewed(tr)), 1)

    def run_with_reviewer(self, verdict, blockers=None, error=None):
        seen = []

        def reviewer(tools, task):
            seen.append(task.instructions)
            if error:
                raise error
            return {"verdict": verdict, "blockers": blockers or [], "seconds": 1.0}
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        lim = Limits()
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(diff_exec(), "env", "/w", lim)), lim, tr,
                       reviewer=reviewer).run(Task("make A return 3"))
        return r, model, tr, seen

    def test_model_review_blockers_replace_the_checklist(self):
        b = [{"lens": "requirements", "file": "a.go", "line": 4, "problem": "A returns 2; the task says 3."}]
        r, model, tr, seen = self.run_with_reviewer("revise", b)
        self.assertEqual((r.status, r.summary), ("finished", "b"))
        self.assertEqual(seen, ["make A return 3"])
        result = model.requests[1][-1]["content"]
        self.assertIn("a reviewer model looked at your change", result)
        self.assertIn("- [requirements] a.go:4 A returns 2; the task says 3.", result)
        self.assertIn("say in the finish summary why it is false", result)
        self.assertTrue(result.endswith(SELF_REVIEW))
        self.assertEqual(records(tr, "model_review")[0]["verdict"], "revise")
        self.assertEqual(self.reviewed(tr), [])

    def test_model_review_approval_still_gets_the_checklist(self):
        r, model, tr, _ = self.run_with_reviewer("approve")
        self.assertEqual((r.status, r.summary), ("finished", "b"))
        self.assertIn(SELF_REVIEW, model.requests[1][-1]["content"])
        self.assertEqual(len(self.reviewed(tr)), 1)

    def test_a_failed_model_review_falls_back_to_the_checklist(self):
        r, model, tr, _ = self.run_with_reviewer("revise", error=OSError("engine down"))
        self.assertIn(SELF_REVIEW, model.requests[1][-1]["content"])
        self.assertEqual(records(tr, "model_review")[0]["error"], "OSError: engine down")

    def test_a_revise_with_no_blockers_falls_back_to_the_checklist(self):
        r, model, tr, _ = self.run_with_reviewer("revise", [])
        self.assertIn(SELF_REVIEW, model.requests[1][-1]["content"])

    def test_self_review_off_ends_at_the_first_finish(self):
        e = diff_exec()
        model = FakeModel([reply(call("finish", summary="done"))])
        lim = Limits(self_review=False)
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(Task("t"))
        self.assertEqual((r.status, r.summary, r.steps), ("finished", "done", 1))
        self.assertEqual(self.reviewed(tr), [])
        self.assertEqual(len(model.requests), 1)

    def test_no_review_when_few_calls_are_left(self):
        # 5 calls are left after step 55 of 60, so the review is skipped.
        script = distinct_calls(55) + [reply(call("finish", summary="done"))]
        e = diff_exec()
        model = FakeModel(script)
        lim = Limits(max_steps=60)
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(Task("t"))
        self.assertEqual((r.status, r.summary, r.steps), ("finished", "done", 56))
        self.assertEqual(self.reviewed(tr)[0]["skipped"], "near a limit")
        self.assertEqual(len([x for x in e.execs if "git diff --stat" in x.argv[-1]]), 0)

    def test_no_review_when_the_time_is_mostly_used(self):
        # The clock gives 80% of 1000 s at the finish, so the review is skipped.
        t = iter([0] * 56 + [800] * 100)
        e = diff_exec()
        model = FakeModel(distinct_calls(55) + [reply(call("finish", summary="done"))])
        lim = Limits(max_steps=60, max_seconds=1000)
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr,
                       clock=lambda: next(t)).run(Task("t"))
        self.assertEqual((r.status, r.summary), ("finished", "done"))
        self.assertEqual(self.reviewed(tr)[0]["skipped"], "near a limit")

    def test_failed_diff_command_still_gives_the_review_text(self):
        class Exec(MemExecution):
            def exec(self, req):
                raise RuntimeError("service down")
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        lim = Limits()
        tr = Transcript(Redactor())
        r = Apprentice(model, Tools(Workspace(Exec(), "env", "/w", lim)), lim, tr).run(Task("t"))
        self.assertEqual((r.status, r.summary), ("finished", "b"))
        result = model.requests[1][-1]["content"]
        self.assertIn("finish rejected: review your change first.", result)
        self.assertIn("(no diff)", result)
        self.assertIn("Review your change before you finish", result)
        self.assertEqual(self.reviewed(tr)[0]["diff_chars"], 0)

    def test_two_runs_each_get_one_review(self):
        # The review flag is state of one run, not of the object.
        e = diff_exec()
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))] * 3)
        lim = Limits()
        tr = Transcript(Redactor())
        a = Apprentice(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr)
        for _ in range(3):
            self.assertEqual(a.run(Task("t")).summary, "b")
        self.assertEqual(len(self.reviewed(tr)), 3)


class RedactionTest(unittest.TestCase):
    def test_secrets_never_reach_transcript_or_context(self):
        os.environ["MAGI_FAKE_TOKEN"] = "env-token-abcdef"
        try:
            r = Redactor([SECRET])
            r.add_env(os.environ)
            tr = Transcript(r)
            m = MemExecution({"/work/repo/.env": f"KEY={SECRET}\nT=env-token-abcdef\n"})
            model = FakeModel([reply(call("read_file", path=".env")), reply(call("finish", summary=f"key is {SECRET}"))])
            lim = Limits(self_review=False)
            Apprentice(model, Tools(Workspace(m, "e", "/work/repo", lim)), lim, tr).run(Task(f"use {SECRET}"))
            body = tr.body().decode()
            self.assertNotIn(SECRET, body)
            self.assertNotIn("env-token-abcdef", body)
            self.assertIn("[REDACTED]", body)
            self.assertNotIn(SECRET, model.requests[1][-1]["content"])  # tool output in context
        finally:
            del os.environ["MAGI_FAKE_TOKEN"]

    def test_secret_named_keys_redacted(self):
        r = Redactor()
        self.assertEqual(r.value({"api_key": "abcdefgh", "name": "abcdefgh"}),
                         {"api_key": "[REDACTED]", "name": "abcdefgh"})


class CompactionTest(unittest.TestCase):
    def test_compaction_shrinks_old_results_and_keeps_pairs(self):
        msgs = [{"role": "system", "content": "s"}, {"role": "user", "content": "task"}]
        for i in range(20):
            msgs.append({"role": "assistant", "content": "", "tool_calls": [
                {"id": f"c{i}", "type": "function",
                 "function": {"name": "write_file", "arguments": json.dumps({"path": "p", "content": "x" * 3000})}}]})
            msgs.append({"role": "tool", "tool_call_id": f"c{i}", "_name": "write_file", "content": "y" * 4000})
        ctx = Context(msgs, budget=6000)
        before = ctx.tokens()
        ctx.compact()
        self.assertLess(ctx.tokens(), 6000)
        self.assertLess(ctx.tokens(), before)
        self.assertEqual(ctx.messages[1]["content"], "task")
        wire = ctx.wire()
        ids = {c["id"] for m in wire if m["role"] == "assistant" for c in m.get("tool_calls", [])}
        for m in wire:
            if m["role"] == "tool":
                self.assertIn(m["tool_call_id"], ids)
            if m["role"] == "assistant":
                for c in m["tool_calls"]:
                    json.loads(c["function"]["arguments"])  # still valid JSON
        self.assertIn("compacted", wire[3]["content"] + wire[4]["content"])

    def test_compaction_keeps_a_finish_summary(self):
        # The model reuses the summary of an earlier finish call for its next
        # finish call, so compaction must not replace it with a stub.
        summary = "I changed a.py and b.py. " * 60
        msgs = [{"role": "system", "content": "s"}, {"role": "user", "content": "task"}]
        for i in range(20):
            msgs.append({"role": "assistant", "content": "", "tool_calls": [
                {"id": f"c{i}", "type": "function",
                 "function": {"name": "finish", "arguments": json.dumps({"summary": summary})}}]})
            msgs.append({"role": "tool", "tool_call_id": f"c{i}", "_name": "finish", "content": "x" * 4000})
        ctx = Context(msgs, budget=6000)
        ctx.compact()
        for m in ctx.wire():
            if m["role"] == "assistant":
                for c in m["tool_calls"]:
                    self.assertEqual(c["function"]["name"], "finish")
                    self.assertEqual(json.loads(c["function"]["arguments"])["summary"], summary)

    def test_drops_old_groups_when_needed(self):
        msgs = [{"role": "system", "content": "s"}, {"role": "user", "content": "task"}]
        for i in range(50):
            msgs.append({"role": "assistant", "content": "a" * 900})
            msgs.append({"role": "user", "content": "continue"})
        ctx = Context(msgs, budget=3000)
        ctx.compact()
        self.assertLessEqual(ctx.tokens(), 3000)
        self.assertTrue(ctx.messages[2]["_note"])

    def test_apprentice_compacts_when_over_budget(self):
        big = "z" * 20000
        a, model, _, tr = apprentice([reply(call("bash", command="x")), reply(call("bash", command="y")),
                                 reply(call("bash", command="z")), reply(call("finish", summary="s"))],
                                files=None, limits=Limits(context_tokens=3000, tool_output_chars=8000,
                                                          self_review=False))

        class Exec(MemExecution):
            def exec(self, req):
                r = super().exec(req)
                r.stdout = big.encode()
                return r
        a.tools.ws.client = Exec()
        a.run(Task("t"))
        self.assertIn("compaction", kinds(tr))
        last = model.requests[-1]
        self.assertLess(sum(len(json.dumps(m)) for m in last), 20000)


if __name__ == "__main__":
    unittest.main()
