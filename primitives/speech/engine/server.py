"""Magi speech engine: one HTTP process per model.

  server.py --engine stt --model-dir DIR   Parakeet TDT 0.6B v3 (sherpa-onnx)
  server.py --engine tts --model-dir DIR   Kokoro 82M
  server.py --engine detect --model-dir DIR   prompt injection detector
                                              (ProtectAI deberta v3 v2, ONNX)

--device auto (the default) runs Kokoro on a GPU when torch sees one (CUDA or
ROCm), else on the CPU. Parakeet always runs on the CPU; it is fast enough.

Endpoints (all except /health need "Authorization: Bearer <token>"):
  GET  /health      {"ok": true, "engine": ..., "device": ..., "busy": bool}
  GET  /voices      {"voices": [...]}                      (tts)
  POST /synthesize  JSON {text, voice?, speed?} -> audio/wav (tts)
                    With "timings": true, JSON {audio (base64 WAV), media_type,
                    words: [[word, start_ms, end_ms], ...], audio_ms, ...}
  POST /transcribe  audio bytes, any ffmpeg format -> JSON  (stt)
  POST /detect      JSON {texts: [...]} -> {scores: [...], ...} (detect)
                    The score of a text is the probability of a prompt
                    injection in its worst window of 512 tokens.

Results carry audio_ms and elapsed_ms (JSON fields, or X-Audio-Ms and
X-Elapsed-Ms headers for audio). The model stays loaded. One lock serializes
inference. Errors are JSON {"error": "..."} without input text.
"""

import argparse
import base64
import hashlib
import hmac
import io
import json
import os
import subprocess
import sys
import threading
import time
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

MAX_AUDIO = 16 << 20
MAX_TEXT = 4000
MAX_SECONDS = 300
STT_RATE = 16000
TTS_RATE = 24000

VOICES = [
    ("af_heart", "Heart", "en-US", "female, warm"),
    ("af_bella", "Bella", "en-US", "female"),
    ("af_nicole", "Nicole", "en-US", "female, soft"),
    ("af_sarah", "Sarah", "en-US", "female"),
    ("af_sky", "Sky", "en-US", "female, light"),
    ("am_fenrir", "Fenrir", "en-US", "male, deep"),
    ("am_michael", "Michael", "en-US", "male"),
    ("am_puck", "Puck", "en-US", "male, bright"),
    ("bf_emma", "Emma", "en-GB", "female"),
    ("bf_isabella", "Isabella", "en-GB", "female"),
    ("bm_george", "George", "en-GB", "male"),
    ("bm_fable", "Fable", "en-GB", "male"),
]
DEFAULT_VOICE = "af_heart"


class Fail(Exception):
    def __init__(self, status, message):
        super().__init__(message)
        self.status = status


def verify(directory, pins):
    for name, expected in pins.items():
        digest = hashlib.sha256()
        with (directory / name).open("rb") as f:
            for chunk in iter(lambda: f.read(1 << 20), b""):
                digest.update(chunk)
        if digest.hexdigest() != expected:
            raise SystemExit(f"model file {name} does not match its pin")


def decode_audio(data):
    """Convert any ffmpeg-readable audio to 16 kHz mono float32 samples."""
    import numpy as np

    try:
        p = subprocess.run(
            ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-i", "pipe:0",
             "-t", str(MAX_SECONDS + 1), "-ac", "1", "-ar", str(STT_RATE), "-f", "s16le", "pipe:1"],
            input=data, capture_output=True, timeout=60)
    except subprocess.TimeoutExpired:
        raise Fail(400, "audio decode timed out")
    if p.returncode != 0 or not p.stdout:
        raise Fail(400, "audio could not be decoded")
    samples = np.frombuffer(p.stdout, dtype="<i2").astype(np.float32) / 32768
    if samples.size > MAX_SECONDS * STT_RATE:
        raise Fail(413, f"audio is longer than {MAX_SECONDS} seconds")
    return samples


MAX_DETECT_TEXTS = 64
MAX_DETECT_CHARS = 256 << 10


def load_detect(directory, threads):
    """A text classifier with a SAFE and an INJECTION label. A long text is
    divided into windows of 512 tokens (with an overlap of 128 tokens); the
    score of the text is the highest INJECTION probability of its windows."""
    import numpy as np
    import onnxruntime as ort
    from tokenizers import Tokenizer

    tok = Tokenizer.from_file(str(directory / "tokenizer.json"))
    cls, sep = tok.token_to_id("[CLS]"), tok.token_to_id("[SEP]")
    labels = json.loads((directory / "config.json").read_text())["id2label"]
    injection = int(next(k for k, v in labels.items() if v == "INJECTION"))
    opts = ort.SessionOptions()
    opts.intra_op_num_threads = threads
    opts.inter_op_num_threads = 1
    sess = ort.InferenceSession(str(directory / "model.onnx"), opts, providers=["CPUExecutionProvider"])
    names = {i.name for i in sess.get_inputs()}
    width, stride = 510, 382

    def windows(text):
        ids = tok.encode(text, add_special_tokens=False).ids
        if not ids:
            return []
        return [[cls] + ids[i:i + width] + [sep] for i in range(0, max(1, len(ids) - width + stride), stride)]

    def detect(texts):
        scores, count = [], 0
        for text in texts:
            best = 0.0
            ws = windows(text)
            for i in range(0, len(ws), 8):
                batch = ws[i:i + 8]
                n = max(len(w) for w in batch)
                ids = np.zeros((len(batch), n), dtype=np.int64)
                mask = np.zeros((len(batch), n), dtype=np.int64)
                for j, w in enumerate(batch):
                    ids[j, :len(w)], mask[j, :len(w)] = w, 1
                feed = {"input_ids": ids, "attention_mask": mask}
                if "token_type_ids" in names:
                    feed["token_type_ids"] = np.zeros_like(ids)
                logits = sess.run(None, feed)[0]
                e = np.exp(logits - logits.max(axis=1, keepdims=True))
                p = e[:, injection] / e.sum(axis=1)
                best = max(best, float(p.max()))
            scores.append(round(best, 4))
            count += len(ws)
        return scores, count
    return detect


def load_stt(directory, threads):
    import sherpa_onnx

    rec = sherpa_onnx.OfflineRecognizer.from_transducer(
        encoder=str(directory / "encoder.int8.onnx"), decoder=str(directory / "decoder.int8.onnx"),
        joiner=str(directory / "joiner.int8.onnx"), tokens=str(directory / "tokens.txt"),
        num_threads=threads, sample_rate=STT_RATE, feature_dim=80,
        decoding_method="greedy_search", model_type="nemo_transducer", provider="cpu")

    def transcribe(samples):
        stream = rec.create_stream()
        stream.accept_waveform(STT_RATE, samples)
        rec.decode_stream(stream)
        return stream.result.text.strip()
    return transcribe


def tts_device(choice):
    import torch
    if choice == "auto":
        return "cuda" if torch.cuda.is_available() else "cpu"
    if choice == "gpu":
        if not torch.cuda.is_available():
            raise SystemExit("--device gpu: torch sees no GPU")
        return "cuda"
    return "cpu"


def load_tts(directory, threads, repo, device):
    import numpy as np
    import torch
    from kokoro import KModel, KPipeline

    torch.set_num_threads(threads)
    torch.set_num_interop_threads(1)
    if device == "cuda" and torch.version.hip:
        # MIOpen fails on Kokoro's packed LSTMs (miopenStatusUnknownError on
        # gfx1102), and its convolutions are slower than the native kernels.
        torch.backends.cudnn.enabled = False
    model = KModel(repo_id=repo, config=str(directory / "config.json"),
                   model=str(directory / "kokoro-v1_0.pth")).to(device).eval()
    pipes = {lang: KPipeline(lang_code=lang, repo_id=repo, model=model, device=device) for lang in "ab"}
    paths = {v[0]: str(directory / "voices" / f"{v[0]}.pt") for v in VOICES}
    for voice, path in paths.items():
        pipes[voice[0]].load_single_voice(path)

    def synthesize(text, voice, speed):
        """Returns the WAV, its length in ms, and the time of each word: a
        list of [word, start_ms, end_ms] in the order of the text. Kokoro
        gives the times of English tokens in each part; a part starts after
        the audio of the earlier parts."""
        torch.manual_seed(1729)
        parts, size, words = [], 0, []
        for r in pipes[voice[0]](text, voice=paths[voice], speed=speed):
            if r.audio is None:
                continue
            a = r.audio.detach().cpu().numpy()
            offset_ms = size * 1000 / TTS_RATE
            for t in r.tokens or []:
                if t.phonemes and t.start_ts is not None and t.end_ts is not None:
                    words.append([t.text, round(offset_ms + t.start_ts * 1000), round(offset_ms + t.end_ts * 1000)])
            size += a.size
            if a.ndim != 1 or not np.isfinite(a).all() or size > MAX_SECONDS * 2 * TTS_RATE:
                raise Fail(500, "synthesis produced invalid audio")
            parts.append(a)
        if not parts:
            raise Fail(400, "text produced no audio")
        pcm = np.round(np.clip(np.concatenate(parts), -1, 1 - 1 / 32768) * 32768).astype("<i2")
        buf = io.BytesIO()
        with wave.open(buf, "wb") as w:
            w.setnchannels(1)
            w.setsampwidth(2)
            w.setframerate(TTS_RATE)
            w.writeframes(pcm.tobytes())
        return buf.getvalue(), pcm.size * 1000 // TTS_RATE, words
    return synthesize


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "magi-speech"

    def log_message(self, fmt, *args):
        if getattr(self, "path", "") == "/health":
            return
        sys.stderr.write(json.dumps({"t": time.time(), "msg": fmt % args}) + "\n")

    def reply(self, status, body, ctype="application/json", headers=None):
        if isinstance(body, dict):
            body = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        for k, v in (headers or {}).items():
            self.send_header(k, str(v))
        if status != 200:  # the request body may be unread
            self.send_header("Connection", "close")
            self.close_connection = True
        self.end_headers()
        self.wfile.write(body)

    def authorized(self):
        got = self.headers.get("Authorization", "")
        return hmac.compare_digest(got.encode(), ("Bearer " + self.server.token).encode())

    def body(self, limit):
        n = int(self.headers.get("Content-Length") or 0)
        if n <= 0:
            raise Fail(400, "request body required")
        if n > limit:
            raise Fail(413, "request body too large")
        return self.rfile.read(n)

    def handle_one(self, method):
        s = self.server
        if method == "GET" and self.path == "/health":
            return self.reply(200, {"ok": True, "engine": s.engine_name, "device": s.device, "busy": s.lock.locked()})
        if not self.authorized():
            return self.reply(401, {"error": "bearer token required"})
        route = (method, self.path, s.kind)
        if route == ("GET", "/voices", "tts"):
            return self.reply(200, {"voices": [
                {"id": i, "name": n, "language": l, "description": d} for i, n, l, d in VOICES]})
        if route == ("POST", "/transcribe", "stt"):
            samples = decode_audio(self.body(MAX_AUDIO))
            with s.lock:
                start = time.monotonic()
                text = s.run(samples)
                elapsed = int((time.monotonic() - start) * 1000)
            return self.reply(200, {"text": text, "engine": s.engine_name,
                                    "audio_ms": samples.size * 1000 // STT_RATE, "elapsed_ms": elapsed})
        if route == ("POST", "/detect", "detect"):
            try:
                req = json.loads(self.body(MAX_DETECT_CHARS * 4))
                texts = [str(t) for t in req["texts"]]
            except (ValueError, TypeError, KeyError):
                raise Fail(400, "invalid JSON request")
            if not 1 <= len(texts) <= MAX_DETECT_TEXTS or sum(len(t) for t in texts) > MAX_DETECT_CHARS:
                raise Fail(400, f"texts must be 1 to {MAX_DETECT_TEXTS} texts with at most {MAX_DETECT_CHARS} characters")
            with s.lock:
                start = time.monotonic()
                scores, windows = s.run(texts)
                elapsed = int((time.monotonic() - start) * 1000)
            return self.reply(200, {"scores": scores, "windows": windows, "engine": s.engine_name, "elapsed_ms": elapsed})
        if route == ("POST", "/synthesize", "tts"):
            try:
                req = json.loads(self.body(64 << 10))
                text, voice = str(req.get("text", "")).strip(), req.get("voice") or DEFAULT_VOICE
                speed = float(req.get("speed") or 1.0)
                timings = bool(req.get("timings"))
            except (ValueError, TypeError, AttributeError):
                raise Fail(400, "invalid JSON request")
            if not text or len(text) > MAX_TEXT:
                raise Fail(400, f"text must have 1 to {MAX_TEXT} characters")
            if voice not in {v[0] for v in VOICES}:
                raise Fail(400, f"unknown voice {voice!r}")
            if not 0.5 <= speed <= 2.0:
                raise Fail(400, "speed must be from 0.5 to 2.0")
            with s.lock:
                start = time.monotonic()
                wav, audio_ms, words = s.run(text, voice, speed)
                elapsed = int((time.monotonic() - start) * 1000)
            if timings:  # JSON with the audio in base64 and the word times
                return self.reply(200, {"audio": base64.b64encode(wav).decode(), "media_type": "audio/wav",
                                        "words": words, "audio_ms": audio_ms, "elapsed_ms": elapsed,
                                        "voice": voice, "engine": s.engine_name})
            return self.reply(200, wav, "audio/wav", {"X-Audio-Ms": audio_ms, "X-Elapsed-Ms": elapsed,
                                                      "X-Voice": voice, "X-Engine": s.engine_name})
        return self.reply(404, {"error": "not found"})

    def dispatch(self, method):
        try:
            self.handle_one(method)
        except Fail as e:
            self.reply(e.status, {"error": str(e)})
        except Exception as e:  # do not echo input text
            self.log_message("inference error: %s", type(e).__name__)
            self.reply(500, {"error": "inference failed"})

    def do_GET(self):
        self.dispatch("GET")

    def do_POST(self):
        self.dispatch("POST")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--engine", choices=["stt", "tts", "detect"], required=True)
    ap.add_argument("--model-dir", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--pins", default=str(Path(__file__).with_name("model-pins.json")))
    ap.add_argument("--listen", default="0.0.0.0")
    ap.add_argument("--port", type=int, default=8092)
    ap.add_argument("--threads", type=int, default=2)
    ap.add_argument("--device", choices=["auto", "cpu", "gpu"], default="auto")
    a = ap.parse_args()

    directory = Path(a.model_dir)
    pins = json.loads(Path(a.pins).read_text())
    token = Path(a.token_file).read_text().strip()
    if len(token) < 32:
        raise SystemExit("token file must hold at least 32 characters")
    start = time.monotonic()
    if a.engine == "stt":
        verify(directory, pins["parakeet"]["sha256"])
        run, name, device = load_stt(directory, a.threads), "parakeet-tdt-0.6b-v3", "cpu"
    elif a.engine == "detect":
        verify(directory, pins["injection"]["sha256"])
        run, name, device = load_detect(directory, a.threads), "protectai-deberta-v3-injection-v2", "cpu"
        run(["Ready."])  # warm up
    else:
        verify(directory, pins["kokoro"]["sha256"])
        device = tts_device(a.device)
        run, name = load_tts(directory, a.threads, pins["kokoro"]["repo"], device), "kokoro-82m"
        run("Ready.", DEFAULT_VOICE, 1.0)  # warm up
    srv = ThreadingHTTPServer((a.listen, a.port), Handler)
    srv.daemon_threads = True
    srv.kind, srv.engine_name, srv.run, srv.token, srv.lock = a.engine, name, run, token, threading.Lock()
    srv.device = device
    print(json.dumps({"msg": "ready", "engine": name, "device": device, "port": a.port,
                      "load_ms": int((time.monotonic() - start) * 1000)}), file=sys.stderr, flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
