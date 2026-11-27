"""The limits of a run: the model var, then the limits that the model
endpoint tells (the model catalog of Magi), then the settings of the worker."""
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

from magi_apprentice.config import Limits, ModelConfig
from magi_apprentice.runner import apply_model_override, fetch_limits
from magi_apprentice.worker import QUESTION_SECONDS, QUESTION_STEPS, question_limits


def told(**limits):
    """An endpoint that tells limits, and the model names that it was asked for."""
    asked = []

    def fetch(cfg):
        asked.append(cfg.name)
        return limits
    return fetch, asked


class EndpointLimits(unittest.TestCase):
    def test_the_context_of_the_engine_sets_the_compaction_limit(self):
        fetch, asked = told(context=65536)
        _, lim = apply_model_override(ModelConfig(), Limits(context_tokens=100000), None, fetch)
        # 91.5 % of the window: the rest is room for the answer.
        self.assertEqual(lim.context_tokens, 59965)
        self.assertEqual(asked, ["magi-coder"])

    def test_the_endpoint_is_asked_for_the_model_of_the_var(self):
        fetch, asked = told(context=131072, work_seconds=7200)
        cfg, lim = apply_model_override(ModelConfig(), Limits(max_seconds=3600), '{"name": "careful"}', fetch)
        self.assertEqual(asked, ["careful"])
        self.assertEqual((cfg.name, lim.context_tokens, lim.max_seconds), ("careful", 119930, 7200))

    def test_the_var_wins(self):
        fetch, _ = told(context=131072, work_seconds=7200)
        _, lim = apply_model_override(ModelConfig(), Limits(), '{"context_tokens": 60000, "max_seconds": 1200}', fetch)
        self.assertEqual((lim.context_tokens, lim.max_seconds), (60000, 1200))

    def test_one_limit_of_the_var_keeps_the_other_of_the_endpoint(self):
        fetch, _ = told(context=131072, work_seconds=7200)
        _, lim = apply_model_override(ModelConfig(), Limits(), '{"context_tokens": 60000}', fetch)
        self.assertEqual((lim.context_tokens, lim.max_seconds), (60000, 7200))

    def test_an_endpoint_with_no_limits_changes_nothing(self):
        for answer in ({}, {"id": "m", "object": "model"}, {"context": 0, "work_seconds": -5},
                       {"context": "65536", "work_seconds": True}):
            _, lim = apply_model_override(ModelConfig(), Limits(context_tokens=100000, max_seconds=3600), None,
                                          lambda cfg: answer)
            self.assertEqual((lim.context_tokens, lim.max_seconds), (100000, 3600), answer)

    def test_max_seconds_in_the_var(self):
        _, lim = apply_model_override(ModelConfig(), Limits(), '{"max_seconds": 10800}')
        self.assertEqual(lim.max_seconds, 10800)

    def test_a_question_keeps_its_own_limits(self):
        fetch, _ = told(work_seconds=7200)
        _, lim = apply_model_override(ModelConfig(), Limits(max_steps=150, max_seconds=3600), None, fetch)
        lim = question_limits(lim)
        self.assertEqual((lim.max_steps, lim.max_seconds), (QUESTION_STEPS, QUESTION_SECONDS))


class Fetch(unittest.TestCase):
    """fetch_limits against a real HTTP server."""

    def setUp(self):
        seen = self.seen = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                seen.append((self.path, self.headers.get("Authorization")))
                if self.path == "/v1/models/careful":
                    body, code = json.dumps({"id": "careful", "context": 131072, "work_seconds": 7200}).encode(), 200
                elif self.path == "/v1/models/a%2Fb":
                    body, code = b'{"context": 4096}', 200
                elif self.path == "/v1/models/text":
                    body, code = b"not JSON", 200
                elif self.path == "/v1/models/list":
                    body, code = b"[1, 2]", 200
                else:
                    body, code = b'{"error": "no such model"}', 404
                self.send_response(code)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass
        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}/v1"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()

    def test_limits_of_a_model(self):
        got = fetch_limits(ModelConfig(base_url=self.base + "/", name="careful", api_key="key-1"))
        self.assertEqual((got["context"], got["work_seconds"]), (131072, 7200))
        self.assertEqual(self.seen, [("/v1/models/careful", "Bearer key-1")])

    def test_the_name_is_one_path_segment(self):
        self.assertEqual(fetch_limits(ModelConfig(base_url=self.base, name="a/b")), {"context": 4096})

    def test_no_limits(self):
        for name in ("unknown", "text", "list"):
            self.assertEqual(fetch_limits(ModelConfig(base_url=self.base, name=name)), {}, name)
        self.assertEqual(fetch_limits(ModelConfig(base_url="", name="careful")), {})
        # No server at the address.
        self.server.shutdown()
        self.server.server_close()
        self.assertEqual(fetch_limits(ModelConfig(base_url=self.base, name="careful")), {})
        self.server = HTTPServer(("127.0.0.1", 0), BaseHTTPRequestHandler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def test_the_run_gets_the_limits(self):
        cfg = ModelConfig(base_url=self.base, name="magi-coder")
        cfg, lim = apply_model_override(cfg, Limits(), '{"name": "careful"}', fetch_limits)
        self.assertEqual((lim.context_tokens, lim.max_seconds), (119930, 7200))


if __name__ == "__main__":
    unittest.main()
