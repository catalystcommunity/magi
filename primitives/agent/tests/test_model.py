import io
import json
import unittest
import urllib.error

import fakes  # noqa: F401  (sets sys.path)

from magi_agent.config import ModelConfig
from magi_agent.model import ChatModel, ModelError, Reply, ToolCall, parse_text_tool_calls

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


def http_error(code: int, headers=None) -> urllib.error.HTTPError:
    return urllib.error.HTTPError("u", code, "err", headers or {}, io.BytesIO(b'{"error":"x"}'))


class Opener:
    def __init__(self, outcomes):
        self.outcomes = list(outcomes)
        self.requests = []

    def __call__(self, req, timeout):
        self.requests.append((req, timeout))
        o = self.outcomes.pop(0)
        if isinstance(o, Exception):
            raise o
        return Resp(json.dumps(o).encode())


def model(opener, **kw) -> tuple[ChatModel, list[float]]:
    slept: list[float] = []
    cfg = ModelConfig(base_url="http://m.example.local/v1", name="m", api_key="sk-test-123456", **kw)
    return ChatModel(cfg, sleep=slept.append, opener=opener), slept


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
        self.assertEqual(req.get_header("User-agent"), "magi-agent/1")
        body = json.loads(req.data)
        self.assertEqual(body["model"], "m")
        self.assertEqual(body["tool_choice"], "auto")
        self.assertEqual(timeout, 300.0)
        self.assertEqual(r.message()["tool_calls"][0]["function"]["name"], "bash")

    def test_retries_429_5xx_and_connection_errors(self):
        op = Opener([http_error(429, {"Retry-After": "2"}), http_error(503),
                     urllib.error.URLError("refused"), TimeoutError("slow"), OK])
        m, slept = model(op)
        r = m.complete([], [])
        self.assertEqual(r.tool_calls[0].id, "x1")
        self.assertEqual(len(slept), 4)
        self.assertEqual(slept[0], 2.0)

    def test_no_retry_on_400(self):
        m, slept = model(Opener([http_error(400)]))
        with self.assertRaises(ModelError):
            m.complete([], [])
        self.assertEqual(slept, [])

    def test_gives_up(self):
        m, slept = model(Opener([http_error(500)] * 3), max_retries=2)
        with self.assertRaisesRegex(ModelError, "3 attempts"):
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
