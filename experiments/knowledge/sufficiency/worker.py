"""Local, tool-free support experiment. Only synthetic admitted text is accepted."""
import json
import os
from pathlib import Path
import sys
import time
import urllib.request

PROMPT = (Path(__file__).resolve().parent / "prompt.txt").read_text()
SCHEMA = {
    "type": "object", "additionalProperties": False,
    "required": ["evidence", "gap", "mode"],
    "properties": {
        "evidence": {"type": "array", "maxItems": 6, "items": {
            "type": "object", "additionalProperties": False, "required": ["source", "quote"],
            "properties": {"source": {"type": "string"}, "quote": {"type": "string"}}}},
        "gap": {"type": "string"},
        "mode": {"type": "string", "enum": ["answer", "partial", "clarify", "unavailable", "no_evidence"]},
    },
}


def post(path, body):
    port = int(os.environ["OPAL_PLANNER_PORT"])
    if not 1 <= port <= 65535:
        raise ValueError("invalid local port")
    request = urllib.request.Request(f"http://127.0.0.1:{port}/{path}", json.dumps(body).encode(), {"Content-Type": "application/json"})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=90) as response:
        data = response.read(1_048_577)
        if len(data) > 1_048_576:
            raise ValueError("oversized model reply")
        return json.loads(data)


print(json.dumps({"ready": True}), flush=True)
while line := sys.stdin.buffer.readline(65537):
    started = time.perf_counter()
    if len(line) > 65536:
        print(json.dumps({"error": "oversized packet"}), flush=True)
        break
    try:
        packet = json.loads(line)
        prompt = post("apply-template", {"messages": [{"role": "system", "content": PROMPT}, {"role": "user", "content": json.dumps(packet)}]})["prompt"]
        tokens = len(post("tokenize", {"content": prompt, "add_special": True})["tokens"])
        if tokens + 640 > 4096:
            raise ValueError("packet exceeds context budget; no silent truncation")
        result = post("completion", {"prompt": prompt, "temperature": 0, "seed": 1, "n_predict": 640, "cache_prompt": False, "json_schema": SCHEMA})
        print(json.dumps({"raw": result["content"], "timings": result.get("timings", {}), "prompt_tokens": tokens,
                          "truncated": result.get("truncated", False), "limit_reached": result.get("stopped_limit", False),
                          "milliseconds": (time.perf_counter() - started) * 1000}), flush=True)
    except Exception:
        # Do not echo an arbitrary exception that can contain request payloads.
        print(json.dumps({"error": "local support inference failed", "milliseconds": (time.perf_counter() - started) * 1000}), flush=True)
