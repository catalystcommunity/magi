import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from fakes import FakeModel, FakeServices, MemExecution, call, reply

from magi_agent._clients import HttpTransport, RpcError, artifacts as art, workflow as wf
from magi_agent.config import Limits, ModelConfig
from magi_agent.model import ModelError
from magi_agent.runner import apply_model_override
from magi_agent.worker import Worker


def assignment(**vars):
    return wf.Assignment(run="run_1", step="code", attempt=2, claim="clm_1", owner="user:a@example.local",
                         vars=vars)


def worker(services, model, **kw):
    return Worker(workflow=wf.WorkflowClient(services), execution=MemExecution(),
                  artifacts=art.ArtifactClient(services), model_cfg=ModelConfig(api_key="key-abcdef-1"),
                  limits=Limits(), model=model, lease_ms=3000, claim_wait_ms=1, **kw)


class WorkerTest(unittest.TestCase):
    def test_claim_run_complete(self):
        s = FakeServices([assignment(environment="env_1", instructions="write x", workdir="/w",
                                     check_output="old failure")])
        model = FakeModel([reply(call("write_file", path="x", content="1")),
                           reply(call("finish", summary="wrote x"))])
        self.assertEqual(worker(s, model, check_command=None).serve(max_tasks=1), 1)
        claim = s.of("claim")[0]
        self.assertEqual((claim.queue, claim.lease_ms), ("agent", 3000))
        done = s.of("complete")[0]
        self.assertEqual((done.run, done.step, done.claim, done.outcome), ("run_1", "code", "clm_1", "done"))
        self.assertEqual(done.outputs["agent.summary"], "wrote x")
        self.assertEqual(done.outputs["agent.steps"], "2")
        digest = done.outputs["agent.transcript"]
        put, _ = s.artifacts[digest]
        self.assertEqual(put.media_type, "application/x-ndjson")
        self.assertEqual(put.labels["kind"], "agent-transcript")
        self.assertEqual((put.labels["run"], put.labels["step"]), ("run_1", "code"))
        self.assertNotIn(b"key-abcdef-1", put.body)
        notes = [p.note for p in s.of("progress")]
        self.assertTrue(notes[0].startswith("agent started"))
        self.assertIn("finished", notes[-1])
        self.assertIn("old failure", model.requests[0][1]["content"])

    def test_default_check_command(self):
        s = FakeServices([assignment(environment="env_1", instructions="t")])
        w = worker(s, FakeModel([reply(call("finish", summary="s"))]))
        w.serve(max_tasks=1)
        self.assertEqual(w.execution.execs[-1].argv, ["bash", "-lc", "./tools/check.sh"])
        self.assertEqual(s.of("complete")[0].outputs["agent.check_passed"], "true")

    def test_missing_vars_fail(self):
        s = FakeServices([assignment(instructions="no env")])
        worker(s, FakeModel([])).serve(max_tasks=1)
        c = s.of("complete")[0]
        self.assertEqual(c.outcome, "fail")
        self.assertIn("environment", c.error)

    def test_model_error_fails_step_with_transcript(self):
        class Broken:
            def complete(self, m, t):
                raise ModelError("HTTP 401: denied")
        s = FakeServices([assignment(environment="e", instructions="t")])
        worker(s, Broken()).serve(max_tasks=1)
        c = s.of("complete")[0]
        self.assertEqual(c.outcome, "fail")
        self.assertIn("401", c.error)
        self.assertTrue(c.outputs["agent.transcript"].startswith("sha256:"))

    def test_heartbeat_and_lost_claim(self):
        s = FakeServices([assignment(environment="e", instructions="t")])
        s.lost = True

        def slow(_msgs):
            import time
            time.sleep(1.6)
            return reply(call("bash", command="true"))
        worker(s, FakeModel([slow, slow, slow])).serve(max_tasks=1)
        self.assertTrue(s.of("heartbeat"))
        self.assertEqual(s.of("complete"), [])  # a lost claim is not completed

    def test_model_override(self):
        cfg, lim = apply_model_override(ModelConfig(), Limits(), '{"name": "big", "max_steps": 7}')
        self.assertEqual((cfg.name, lim.max_steps), ("big", 7))
        cfg, _ = apply_model_override(ModelConfig(base_url="http://a"), Limits(), "other")
        self.assertEqual((cfg.name, cfg.base_url), ("other", "http://a"))


class HttpTransportTest(unittest.TestCase):
    """The worker through the real HTTP transport and bearer token."""

    def test_worker_over_http(self):
        s = FakeServices([assignment(environment="e", instructions="t")])
        seen = []

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                seen.append((self.path, self.headers.get("Authorization")))
                body = self.rfile.read(int(self.headers["Content-Length"]))
                _, _, svc, op = self.path.split("/")
                try:
                    out, code, ctype = s.call(svc, op, body), 200, "application/cbor"
                except RpcError as e:
                    out, code, ctype = json.dumps({"code": e.code, "message": e.message}).encode(), e.code, "application/json"
                self.send_response(code)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(out)))
                self.end_headers()
                self.wfile.write(out)

        srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            t = HttpTransport(f"http://127.0.0.1:{srv.server_port}", token=lambda: "tok-123456")
            w = Worker(workflow=wf.WorkflowClient(t), execution=MemExecution(), artifacts=art.ArtifactClient(t),
                       model_cfg=ModelConfig(), limits=Limits(), lease_ms=3000, claim_wait_ms=1,
                       model=FakeModel([reply(call("finish", summary="ok"))]), check_command=None)
            w.serve(max_tasks=1)
        finally:
            srv.shutdown()
        self.assertEqual(s.of("complete")[0].outcome, "done")
        self.assertIn(("/rpc/WorkflowService/claim", "Bearer tok-123456"), seen)
        self.assertIn("/rpc/ArtifactService/put", [p for p, _ in seen])


if __name__ == "__main__":
    unittest.main()
