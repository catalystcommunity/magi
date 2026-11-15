import json
import os
import unittest

from fakes import FakeModel, MemExecution, call, reply

from magi_apprentice.apprentice import SELF_REVIEW, Apprentice, Context, Task
from magi_apprentice.config import Limits
from magi_apprentice.model import ModelError
from magi_apprentice.tools import Tools, Workspace
from magi_apprentice.transcript import Redactor, Transcript

SECRET = "sk-very-secret-987654"


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

    def test_stalls_without_tool_calls(self):
        a, model, _, _ = apprentice([reply(content="thinking")] * 3)
        r = a.run(Task("t"))
        self.assertEqual(r.status, "stalled")
        self.assertIn("call finish", model.requests[1][-1]["content"])

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

    def test_model_error_is_reported(self):
        class Broken:
            def complete(self, m, t):
                raise ModelError("HTTP 401: bad key")
        lim = Limits()
        r = Apprentice(Broken(), Tools(Workspace(MemExecution(), "e", "/w", lim)), lim, Transcript(Redactor())).run(Task("t"))
        self.assertEqual(r.status, "error")
        self.assertIn("401", r.error)

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
        script = [reply(call("bash", command="true"))] * 10
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
        a, model, _, tr = apprentice([reply(call("bash", command="true"))] * 20,
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
        script = [reply(call("bash", command="true"))] * 55 + [reply(call("finish", summary="done"))]
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
        model = FakeModel([reply(call("bash", command="true"))] * 55
                          + [reply(call("finish", summary="done"))])
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
