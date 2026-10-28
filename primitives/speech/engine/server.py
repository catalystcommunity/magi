"""Magi speech engine: one HTTP process per model.

  server.py --engine stt --model-dir DIR   Parakeet TDT 0.6B v3 (sherpa-onnx)
  server.py --engine tts --model-dir DIR   Kokoro 82M

Endpoints (all except /health need "Authorization: Bearer <token>"):
  GET  /health      {"ok": true, "engine": ..., "busy": bool}
  GET  /voices      {"voices": [...]}                      (tts)
  POST /synthesize  JSON {text, voice?, speed?} -> audio/wav (tts)
  POST /transcribe  audio bytes, any ffmpeg format -> JSON  (stt)

Results carry audio_ms and elapsed_ms (JSON fields, or X-Audio-Ms and
X-Elapsed-Ms headers for audio). The model stays loaded. One lock serializes
inference. Errors are JSON {"error": "..."} without input text.
"""

import argparse
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


def load_tts(directory, threads, repo):
    import numpy as np
    import torch
    from kokoro import KModel, KPipeline

    torch.set_num_threads(threads)
    torch.set_num_interop_threads(1)
    model = KModel(repo_id=repo, config=str(directory / "config.json"),
                   model=str(directory / "kokoro-v1_0.pth")).to("cpu").eval()
    pipes = {lang: KPipeline(lang_code=lang, repo_id=repo, model=model, device="cpu") for lang in "ab"}
    paths = {v[0]: str(directory / "voices" / f"{v[0]}.pt") for v in VOICES}
    for voice, path in paths.items():
        pipes[voice[0]].load_single_voice(path)

    def synthesize(text, voice, speed):
        torch.manual_seed(1729)
        parts, size = [], 0
        for r in pipes[voice[0]](text, voice=paths[voice], speed=speed):
            if r.audio is None:
                continue
            a = r.audio.detach().cpu().numpy()
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
        return buf.getvalue(), pcm.size * 1000 // TTS_RATE
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
            return self.reply(200, {"ok": True, "engine": s.engine_name, "busy": s.lock.locked()})
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
        if route == ("POST", "/synthesize", "tts"):
            try:
                req = json.loads(self.body(64 << 10))
                text, voice = str(req.get("text", "")).strip(), req.get("voice") or DEFAULT_VOICE
                speed = float(req.get("speed") or 1.0)
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
                wav, audio_ms = s.run(text, voice, speed)
                elapsed = int((time.monotonic() - start) * 1000)
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
    ap.add_argument("--engine", choices=["stt", "tts"], required=True)
    ap.add_argument("--model-dir", required=True)
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--pins", default=str(Path(__file__).with_name("model-pins.json")))
    ap.add_argument("--listen", default="0.0.0.0")
    ap.add_argument("--port", type=int, default=8092)
    ap.add_argument("--threads", type=int, default=2)
    a = ap.parse_args()

    directory = Path(a.model_dir)
    pins = json.loads(Path(a.pins).read_text())
    token = Path(a.token_file).read_text().strip()
    if len(token) < 32:
        raise SystemExit("token file must hold at least 32 characters")
    start = time.monotonic()
    if a.engine == "stt":
        verify(directory, pins["parakeet"]["sha256"])
        run, name = load_stt(directory, a.threads), "parakeet-tdt-0.6b-v3"
    else:
        verify(directory, pins["kokoro"]["sha256"])
        run, name = load_tts(directory, a.threads, pins["kokoro"]["repo"]), "kokoro-82m"
        run("Ready.", DEFAULT_VOICE, 1.0)  # warm up
    srv = ThreadingHTTPServer((a.listen, a.port), Handler)
    srv.daemon_threads = True
    srv.kind, srv.engine_name, srv.run, srv.token, srv.lock = a.engine, name, run, token, threading.Lock()
    print(json.dumps({"msg": "ready", "engine": name, "port": a.port,
                      "load_ms": int((time.monotonic() - start) * 1000)}), file=sys.stderr, flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
