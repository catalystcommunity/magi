import io
import socket
import time
import json
import unittest
import urllib.error

import fakes  # noqa: F401  (sets sys.path)

from magi_apprentice.config import ModelConfig
from magi_apprentice.model import ChatModel, ModelError, Reply, ToolCall, parse_text_tool_calls

OK = {"choices": [{"finish_reason": "tool_calls", "message": {
    "role": "assistant", "content": "",
    "tool_calls": [{"id": "x1", "type": "function",
                    "function": {"name": "bash", "arguments": "{\"command\":\"ls\"}"}}]}}],
    "usage": {"prompt_tokens": 50, "completion_tokens": 5}}


class Resp(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False


def http_error(code: int, headers=None, body: bytes = b'{"error":"x"}') -> urllib.error.HTTPError:
    return urllib.error.HTTPError("u", code, "err", headers or {}, io.BytesIO(body))


def busy(reason: str, retry_after: str | None = None) -> urllib.error.HTTPError:
    """A 503 from a busy inference host: a JSON body with error.type, and a
    Retry-After header with the seconds until the host serves again."""
    body = json.dumps({"error": {"type": reason, "message": "the engine is stopped"}}).encode()
    return http_error(503, {"Retry-After": retry_after} if retry_after else None, body)


class Opener:
    def __init__(self, outcomes):
        self.outcomes = list(outcomes)
        self.requests = []

    def __call__(self, req, timeout):
        self.requests.append((req, timeout))
        o = self.outcomes.pop(0)
        if isinstance(o, Exception):
            raise o
        if isinstance(o, bytes):
            return Resp(o)
        return Resp(json.dumps(o).encode())


def sse(*events) -> bytes:
    return b"".join(b"data: " + json.dumps(e).encode() + b"\n\n" for e in events) + b"data: [DONE]\n\n"


STREAM = sse(
    {"choices": [{"index": 0, "delta": {"role": "assistant", "content": "Look"}}]},
    {"choices": [{"index": 0, "delta": {"content": "ing."}}]},
    {"choices": [{"index": 0, "delta": {"tool_calls": [{"index": 0, "id": "c1", "type": "function",
                                                         "function": {"name": "read_file", "arguments": "{\"pa"}}]}}]},
    {"choices": [{"index": 0, "delta": {"tool_calls": [{"index": 0, "function": {"arguments": "th\": \"a.go\"}"}}]}}]},
    {"choices": [{"index": 0, "delta": {"tool_calls": [{"index": 1, "id": "c2", "type": "function",
                                                         "function": {"name": "bash", "arguments": "{}"}}]}}]},
    {"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}]},
    {"choices": [], "usage": {"prompt_tokens": 9, "completion_tokens": 4}})


def model(opener, on_wait=None, **kw) -> tuple[ChatModel, list[float]]:
    slept: list[float] = []
    cfg = ModelConfig(base_url="http://m.example.local/v1", name="m", api_key="sk-test-123456", **kw)
    return ChatModel(cfg, sleep=slept.append, opener=opener, on_wait=on_wait), slept


class ModelTest(unittest.TestCase):
    def test_parses_tool_calls_and_sends_auth(self):
        op = Opener([OK])
        m, _ = model(op)
        r = m.complete([{"role": "user", "content": "hi"}], [{"type": "function"}])
        self.assertEqual(r.tool_calls[0].name, "bash")
        self.assertEqual(json.loads(r.tool_calls[0].arguments), {"command": "ls"})
        req, timeout = op.requests[0]
        self.assertEqual(req.full_url, "http://m.example.local/v1/chat/completions")
        self.assertEqual(req.get_header("Authorization"), "Bearer sk-test-123456")
        self.assertEqual(req.get_header("User-agent"), "magi-apprentice/1")
        body = json.loads(req.data)
        self.assertEqual(body["model"], "m")
        self.assertEqual(body["tool_choice"], "auto")
        self.assertTrue(body["stream"])
        self.assertEqual(timeout, 600.0)  # the wait for the start of the answer
        self.assertEqual(r.message()["tool_calls"][0]["function"]["name"], "bash")

    def test_retries_429_5xx_and_connection_errors(self):
        op = Opener([http_error(429, {"Retry-After": "2"}), http_error(524),
                     urllib.error.URLError("refused"), TimeoutError("slow"), OK])
        m, slept = model(op)
        r = m.complete([], [])
        self.assertEqual(r.tool_calls[0].id, "x1")
        self.assertEqual(len(slept), 4)
        self.assertEqual(slept[0], 2.0)

    def test_joins_a_streamed_answer(self):
        m, _ = model(Opener([STREAM]))
        r = m.complete([], [{"type": "function"}])
        self.assertEqual(r.content, "Looking.")
        self.assertEqual([(c.id, c.name) for c in r.tool_calls], [("c1", "read_file"), ("c2", "bash")])
        self.assertEqual(json.loads(r.tool_calls[0].arguments), {"path": "a.go"})
        self.assertEqual(r.finish_reason, "tool_calls")
        self.assertEqual(r.usage["completion_tokens"], 4)

    def test_reads_the_reasoning_of_a_full_reply(self):
        # A thinking model sends its reasoning next to the content. Providers
        # do not agree on the field name, so both are read.
        data = {"choices": [{"finish_reason": "length", "message": {
            "role": "assistant", "content": "", "reasoning_content": "I must check the tests first."}}]}
        m, _ = model(Opener([data]))
        r = m.complete([], [])
        self.assertEqual(r.reasoning, "I must check the tests first.")
        self.assertEqual(r.content, "")
        data["choices"][0]["message"] = {"role": "assistant", "content": "", "reasoning": "Other field."}
        m2, _ = model(Opener([data]))
        self.assertEqual(m2.complete([], []).reasoning, "Other field.")
        # the reasoning does not go back to the model in the next request
        self.assertEqual(r.message(), {"role": "assistant", "content": ""})

    def test_reads_the_reasoning_of_a_streamed_answer(self):
        stream = sse(
            {"choices": [{"index": 0, "delta": {"role": "assistant", "reasoning_content": "Step one."}}]},
            {"choices": [{"index": 0, "delta": {"reasoning": " Step two."}}]},
            {"choices": [{"index": 0, "delta": {"content": ""}, "finish_reason": "length"}]},
            {"choices": [], "usage": {"prompt_tokens": 9, "completion_tokens": 16384}})
        m, _ = model(Opener([stream]))
        r = m.complete([], [])
        self.assertEqual(r.reasoning, "Step one. Step two.")
        self.assertEqual(r.content, "")
        self.assertEqual(r.finish_reason, "length")
        self.assertEqual(r.message(), {"role": "assistant", "content": ""})

    def test_retries_a_provider_that_stops_sending(self):
        # The socket timeout (idle_s) is raised while the answer is read.
        op = Opener([socket.timeout("timed out"), STREAM])
        m, slept = model(op)
        self.assertEqual(m.complete([], []).tool_calls[0].name, "read_file")
        self.assertEqual(len(slept), 1)

    def test_no_stream(self):
        op = Opener([OK])
        m, _ = model(op, stream=False)
        m.complete([], [])
        req, timeout = op.requests[0]
        self.assertNotIn("stream", json.loads(req.data))
        self.assertEqual(timeout, 900.0)

    def test_idle_limit_on_a_real_socket(self):
        # The server sends the start of the answer, then stops. The read
        # must fail after idle_s, not after first_s.
        import http.server
        import threading
        stop = threading.Event()

        class H(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b'data: {"choices":[{"delta":{"content":"a"}}]}\n\n')
                self.wfile.flush()
                stop.wait(10)

            def log_message(self, *a):
                pass

        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            cfg = ModelConfig(base_url=f"http://127.0.0.1:{srv.server_port}/v1", name="m", first_s=5, idle_s=0.3, max_retries=0)
            start = time.monotonic()
            with self.assertRaisesRegex(ModelError, "timed out"):
                ChatModel(cfg, sleep=lambda s: None).complete([], [])
            self.assertLess(time.monotonic() - start, 3)
        finally:
            stop.set()
            srv.shutdown()

    def test_no_retry_on_400(self):
        m, slept = model(Opener([http_error(400)]))
        with self.assertRaises(ModelError):
            m.complete([], [])
        self.assertEqual(slept, [])

    def test_gives_up(self):
        m, slept = model(Opener([http_error(500)] * 3), max_retries=2)
        with self.assertRaisesRegex(ModelError, "3 attempts"):
            m.complete([], [])

    def test_busy_waits_do_not_use_up_the_retries(self):
        # A busy host answers 503 for as long as the machine is busy. The waits
        # count against the busy budget only, so a call that waits more than
        # max_retries times still gets its answer.
        op = Opener([busy("host_busy", "60") for _ in range(7)] + [OK])
        waits: list[tuple[float, str]] = []
        m, slept = model(op, on_wait=lambda s, r: waits.append((s, r)), max_retries=2, busy_wait_s=600)
        r = m.complete([], [])
        self.assertEqual(r.tool_calls[0].id, "x1")
        self.assertEqual(slept, [60.0] * 7)
        self.assertEqual(waits, [(60.0, "host_busy")] * 7)

    def test_busy_wait_uses_the_default_when_there_is_no_retry_after(self):
        op = Opener([busy("host_busy"), OK])
        m, slept = model(op)
        m.complete([], [])
        self.assertEqual(slept, [30.0])

    def test_busy_wait_is_capped_at_120_s(self):
        op = Opener([busy("host_busy", "600"), OK])
        m, slept = model(op)
        m.complete([], [])
        self.assertEqual(slept, [120.0])

    def test_warming_uses_its_retry_after(self):
        # While the engine starts, the host answers 503 with type "warming" and
        # a short Retry-After.
        op = Opener([busy("warming", "15"), OK])
        waits: list[tuple[float, str]] = []
        m, _ = model(op, on_wait=lambda s, r: waits.append((s, r)))
        m.complete([], [])
        self.assertEqual(waits, [(15.0, "warming")])

    def test_the_busy_budget_ends_the_waits(self):
        # The waits stop when they reach busy_wait_s in total. The error keeps
        # the last message of the host.
        op = Opener([busy("host_busy", "60") for _ in range(10)])
        m, _ = model(op, busy_wait_s=150)
        with self.assertRaisesRegex(ModelError, "busy for 120 s.*the limit is 150 s.*host_busy"):
            m.complete([], [])
        self.assertEqual(len(op.requests), 3)  # 60 + 60 + 60 would pass 150

    def test_a_503_without_a_busy_body_is_a_retry(self):
        # A plain 503 from a provider is a retry as before, not a wait.
        op = Opener([http_error(503, {"Retry-After": "2"}), OK])
        m, slept = model(op, max_retries=1)
        m.complete([], [])
        self.assertEqual(slept, [2.0])

    def test_bad_response_shows_the_start_of_the_body(self):
        # A host that answers HTML or text gives no JSON to parse. The error
        # shows the start of the body, so the evidence says what the host sent.
        # Control characters do not go in: they make the error unreadable.
        op = Opener([b"<html>Gateway\x00\n error</html>"] * 3)
        m, _ = model(op, max_retries=2)
        with self.assertRaisesRegex(ModelError, "bad response: .*<html>Gateway error</html>"):
            m.complete([], [])

    def test_text_tool_call_fallback(self):
        text = ("ok\n<tool_call>\n<function=write_file>\n<parameter=path>\na.py\n</parameter>\n"
                "<parameter=content>\nx = 1\n</parameter>\n</function>\n</tool_call>")
        rest, calls = parse_text_tool_calls(text)
        self.assertEqual(rest, "ok")
        self.assertEqual(json.loads(calls[0].arguments), {"path": "a.py", "content": "x = 1"})
        rest, calls = parse_text_tool_calls('<tool_call>{"name": "finish", "arguments": {"summary": "s"}}</tool_call>')
        self.assertEqual(calls[0].name, "finish")
        data = {"choices": [{"message": {"content": text}}]}
        m, _ = model(Opener([data]))
        self.assertEqual(m.complete([], []).tool_calls[0].name, "write_file")

    def test_history_replaces_invalid_arguments(self):
        r = Reply("", [ToolCall("a", "bash", '{"command": "ls'), ToolCall("b", "bash", '{"command": "ls"}')])
        args = [c["function"]["arguments"] for c in r.message()["tool_calls"]]
        self.assertEqual(args, ["{}", '{"command": "ls"}'])
        self.assertEqual(r.tool_calls[0].arguments, '{"command": "ls')


if __name__ == "__main__":
    unittest.main()


class BudgetErrorTest(unittest.TestCase):
    """An answer that says the money or the quota is used up ends the call at
    once: retries cannot help, and each one fails a queued task."""

    BUDGET = json.dumps({"type": "error", "error": {"type": "BudgetLimitError",
                                                    "message": "Spend limit exhausted."},
                         "metadata": {"resetsAt": "2026-12-01T00:00:00.000Z"}}).encode()

    def test_a_budget_429_is_not_retried(self):
        op = Opener([http_error(429, {"Retry-After": "2"}, self.BUDGET), OK])
        m, slept = model(op)
        with self.assertRaises(ModelError) as cm:
            m.complete([{"role": "user", "content": "hi"}], [])
        self.assertEqual(cm.exception.kind, "budget")
        self.assertIn("Spend limit exhausted", str(cm.exception))
        self.assertEqual((len(op.requests), slept), (1, []))

    def test_a_402_is_a_budget_error(self):
        op = Opener([http_error(402, None, b'{"error":{"message":"Insufficient balance"}}')])
        m, _ = model(op)
        with self.assertRaises(ModelError) as cm:
            m.complete([{"role": "user", "content": "hi"}], [])
        self.assertEqual((cm.exception.kind, len(op.requests)), ("budget", 1))

    def test_a_rate_limit_429_is_still_retried(self):
        op = Opener([http_error(429, {"Retry-After": "2"}, b'{"error":{"type":"rate_limit_exceeded"}}'), OK])
        m, slept = model(op)
        m.complete([{"role": "user", "content": "hi"}], [])
        self.assertEqual((len(op.requests), slept), (2, [2.0]))

    def test_used_up_retries_are_an_unavailable_endpoint(self):
        op = Opener([http_error(524)] * 3)
        m, _ = model(op, max_retries=2)
        with self.assertRaises(ModelError) as cm:
            m.complete([{"role": "user", "content": "hi"}], [])
        self.assertEqual((cm.exception.kind, len(op.requests)), ("unavailable", 3))
