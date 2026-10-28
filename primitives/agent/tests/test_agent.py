import json
import os
import unittest

from fakes import FakeModel, MemExecution, call, reply

from magi_agent.agent import Agent, Context, Task
from magi_agent.config import Limits
from magi_agent.model import ModelError
from magi_agent.tools import Tools, Workspace
from magi_agent.transcript import Redactor, Transcript

SECRET = "sk-very-secret-987654"


def agent(script, files=None, limits=None, **kw):
    lim = limits or Limits()
    m = MemExecution(files)
    model = FakeModel(script)
    tr = Transcript(Redactor([SECRET]))
    return Agent(model, Tools(Workspace(m, "env", "/work/repo", lim)), lim, tr, **kw), model, m, tr


def kinds(tr):
    return [json.loads(line)["kind"] for line in tr.records]


class LoopTest(unittest.TestCase):
    def test_write_then_finish(self):
        a, model, m, tr = agent([reply(call("write_file", path="x.py", content="x = 1\n")),
                                 reply(call("finish", summary="wrote x.py"))])
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
        a, model, _, _ = agent([reply(call("finish", summary="s"))])
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
        lim = Limits()
        e = Exec()
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        tr = Transcript(Redactor())
        r = Agent(model, Tools(Workspace(e, "env", "/w", lim)), lim, tr).run(Task("t", check_command="./check.sh"))
        self.assertEqual((r.status, r.summary, r.check_passed), ("finished", "b", True))
        self.assertIn("finish rejected", model.requests[1][-1]["content"])

    def test_check_gate_gives_up_after_retries(self):
        class Exec(MemExecution):
            def exec(self, req):
                r = super().exec(req)
                r.exit_code = 1
                return r
        lim = Limits(check_retries=2)
        model = FakeModel([reply(call("finish", summary="a")), reply(call("finish", summary="b"))])
        r = Agent(model, Tools(Workspace(Exec(), "env", "/w", lim)), lim, Transcript(Redactor())).run(
            Task("t", check_command="false"))
        self.assertEqual((r.status, r.check_passed), ("finished", False))

    def test_stalls_without_tool_calls(self):
        a, model, _, _ = agent([reply(content="thinking")] * 3)
        r = a.run(Task("t"))
        self.assertEqual(r.status, "stalled")
        self.assertIn("call finish", model.requests[1][-1]["content"])

    def test_step_limit(self):
        a, _, _, _ = agent([reply(call("bash", command="true"))] * 10, limits=Limits(max_steps=3))
        r = a.run(Task("t"))
        self.assertEqual((r.status, r.steps), ("step_limit", 3))

    def test_time_limit(self):
        t = iter(range(0, 10000, 1000))
        a, _, _, _ = agent([reply(call("bash", command="true"))] * 10,
                           limits=Limits(max_seconds=2500), clock=lambda: next(t))
        self.assertEqual(a.run(Task("t")).status, "time_limit")

    def test_should_stop(self):
        a, _, _, _ = agent([reply(call("bash", command="true"))], should_stop=lambda: "cancelled")
        self.assertEqual(a.run(Task("t")).status, "stopped")

    def test_steering_notes_reach_the_model(self):
        notes = [["use Go, not Python"], []]
        a, model, _, tr = agent([reply(call("finish", summary="s"))], steering=lambda: notes.pop(0))
        a.run(Task("t"))
        self.assertIn("Operator steering note: use Go, not Python", model.requests[0][-1]["content"])
        self.assertIn("steering", kinds(tr))

    def test_progress_every_n_tool_calls(self):
        notes = []
        a, _, _, _ = agent([reply(call("bash", command="echo 1"), call("bash", command="echo 2")),
                            reply(call("finish", summary="s"))],
                           limits=Limits(progress_every=2), on_progress=notes.append)
        a.run(Task("t"))
        self.assertEqual(len(notes), 1)
        self.assertIn("bash echo 1; bash echo 2", notes[0])

    def test_model_error_is_reported(self):
        class Broken:
            def complete(self, m, t):
                raise ModelError("HTTP 401: bad key")
        lim = Limits()
        r = Agent(Broken(), Tools(Workspace(MemExecution(), "e", "/w", lim)), lim, Transcript(Redactor())).run(Task("t"))
        self.assertEqual(r.status, "error")
        self.assertIn("401", r.error)


class RedactionTest(unittest.TestCase):
    def test_secrets_never_reach_transcript_or_context(self):
        os.environ["MAGI_FAKE_TOKEN"] = "env-token-abcdef"
        try:
            r = Redactor([SECRET])
            r.add_env(os.environ)
            tr = Transcript(r)
            m = MemExecution({"/work/repo/.env": f"KEY={SECRET}\nT=env-token-abcdef\n"})
            model = FakeModel([reply(call("read_file", path=".env")), reply(call("finish", summary=f"key is {SECRET}"))])
            lim = Limits()
            Agent(model, Tools(Workspace(m, "e", "/work/repo", lim)), lim, tr).run(Task(f"use {SECRET}"))
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

    def test_agent_compacts_when_over_budget(self):
        big = "z" * 20000
        a, model, _, tr = agent([reply(call("bash", command="x")), reply(call("bash", command="y")),
                                 reply(call("bash", command="z")), reply(call("finish", summary="s"))],
                                files=None, limits=Limits(context_tokens=3000, tool_output_chars=8000))

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
