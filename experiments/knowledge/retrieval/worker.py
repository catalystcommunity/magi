"""CPU-only experiment worker. It receives text, never relevance labels or IDs."""
import hashlib
import json
import math
import os
from pathlib import Path
import sys
import time
import urllib.request

os.environ["TOKENIZERS_PARALLELISM"] = "false"
os.environ["HF_HUB_OFFLINE"] = "1"
import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

ROOT = Path(__file__).resolve().parent
PIN = json.loads((ROOT / "model.json").read_text())
MODEL = ROOT.parent / ".cache" / "minilm"
PLANNER = (ROOT / "planner.txt").read_text()


def verify_files():
    model = (MODEL / "model.onnx").read_bytes()
    if len(model) != PIN["size"] or hashlib.sha256(model).hexdigest() != PIN["sha256"]:
        raise ValueError("model hash mismatch")
    tokenizer = (MODEL / "tokenizer.json").read_bytes()
    blob = f"blob {len(tokenizer)}\0".encode() + tokenizer
    if hashlib.sha1(blob).hexdigest() != PIN["tokenizer_git_blob"]:
        raise ValueError("tokenizer hash mismatch")


verify_files()
options = ort.SessionOptions()
options.intra_op_num_threads = PIN["threads"]
options.inter_op_num_threads = 1
session = ort.InferenceSession(str(MODEL / "model.onnx"), sess_options=options, providers=["CPUExecutionProvider"])
tokenizer = Tokenizer.from_file(str(MODEL / "tokenizer.json"))
tokenizer.enable_padding(pad_id=0, pad_token="[PAD]")
tokenizer.enable_truncation(max_length=PIN["max_tokens"])


def embeddings(texts):
    if not isinstance(texts, list) or not 1 <= len(texts) <= 64:
        raise ValueError("invalid batch size")
    if any(not isinstance(t, str) or len(t) > 20000 for t in texts):
        raise ValueError("invalid text")
    # Reject truncation, rather than silently claim a complete representation.
    tokenizer.no_truncation()
    lengths = [len(e.ids) for e in tokenizer.encode_batch(texts)]
    tokenizer.enable_truncation(max_length=PIN["max_tokens"])
    if max(lengths) > PIN["max_tokens"]:
        raise ValueError("text exceeds model token limit; chunk it explicitly")
    encoded = tokenizer.encode_batch(texts)
    feed = {
        "input_ids": np.asarray([e.ids for e in encoded], dtype=np.int64),
        "attention_mask": np.asarray([e.attention_mask for e in encoded], dtype=np.int64),
        "token_type_ids": np.asarray([e.type_ids for e in encoded], dtype=np.int64),
    }
    tokens = session.run(None, {i.name: feed[i.name] for i in session.get_inputs()})[0]
    mask = feed["attention_mask"][..., None].astype(np.float32)
    pooled = (tokens * mask).sum(axis=1) / mask.sum(axis=1).clip(min=1)
    pooled /= np.linalg.norm(pooled, axis=1, keepdims=True).clip(min=1e-12)
    if pooled.shape != (len(texts), PIN["dimensions"]) or not np.isfinite(pooled).all():
        raise ValueError("invalid model output")
    return {"vectors": pooled.tolist(), "tokens": sum(lengths)}


def post(path, body):
    port = int(os.environ["OPAL_PLANNER_PORT"])
    if not 1 <= port <= 65535:
        raise ValueError("invalid local port")
    request = urllib.request.Request(f"http://127.0.0.1:{port}/{path}", json.dumps(body).encode(), {"Content-Type": "application/json"})
    # Do not send local requests through proxy environment settings.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=45) as response:
        data = response.read(1_048_577)
        if len(data) > 1_048_576:
            raise ValueError("oversized model reply")
        return json.loads(data)


def plan(question):
    if not isinstance(question, str) or not 1 <= len(question) <= 512:
        raise ValueError("invalid question")
    prompt = post("apply-template", {"messages": [{"role": "system", "content": PLANNER}, {"role": "user", "content": question}]})["prompt"]
    result = post("completion", {
        "prompt": prompt, "temperature": 0, "seed": 1, "n_predict": 128,
        "cache_prompt": False,
        "json_schema": {"type": "object", "properties": {"queries": {"type": "array", "minItems": 1, "maxItems": 3, "items": {"type": "string"}}}, "required": ["queries"], "additionalProperties": False},
    })
    decoded = json.loads(result["content"])
    queries = decoded["queries"]
    if not 1 <= len(queries) <= 3 or any(not isinstance(q, str) or not 1 <= len(q) <= 200 for q in queries):
        raise ValueError("invalid plan")
    # The store uses plainto_tsquery; model output cannot become SQL or policy.
    return {"queries": queries, "raw": result["content"], "timings": result.get("timings", {})}


print(json.dumps({"ready": True, "provider": session.get_providers(), "onnxruntime": ort.__version__, "numpy": np.__version__}), flush=True)
for line in sys.stdin:
    started = time.perf_counter()
    try:
        if len(line) > 1_048_576:
            raise ValueError("oversized worker request")
        request = json.loads(line)
        if request["op"] == "embed":
            result = embeddings(request["texts"])
        elif request["op"] == "plan":
            result = plan(request["question"])
        else:
            raise ValueError("unknown operation")
        result["milliseconds"] = (time.perf_counter() - started) * 1000
        print(json.dumps(result, allow_nan=False), flush=True)
    except Exception as error:
        print(json.dumps({"error": str(error), "milliseconds": (time.perf_counter() - started) * 1000}), flush=True)
