"""Magi speech engine: one HTTP process per model.

  server.py --engine stt --model-dir DIR   Parakeet TDT 0.6B v3 (sherpa-onnx)
  server.py --engine tts --model-dir DIR   Kokoro 82M
  server.py --engine detect --model-dir DIR   prompt injection detector
                                              (ProtectAI deberta v3 v2, ONNX)
  server.py --engine turn --model-dir DIR     Silero VAD and the turn
                                              classifier (turn/, ONNX)
  server.py --engine speaker --model-dir DIR  voice prints (3D-Speaker
                                              ERes2Net) and target speaker
                                              extraction (VoxLock), both
                                              with sherpa-onnx on the CPU

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
  POST /vad         audio bytes, any ffmpeg format -> JSON {speech_ms,
                    audio_ms, segments: [[start_ms, end_ms], ...]} (turn)
  POST /turn        JSON {items: [{context, text}, ...]} -> {results:
                    [{ack, turn, unclear}, ...]} (turn). context is what the
                    persona said last, text what the person said over it.
  POST /speaker     JSON {audio (base64, any ffmpeg format), sample?
                    (base64: speech of one person)} -> JSON {vector (base64:
                    the voice print of the audio, little-endian float32 with
                    unit length; null when the audio has too little sound),
                    model, audio_ms} (speaker). With sample, when the
                    extraction model is there and the sample has 3 s or
                    more: also extracted (base64 WAV, 16 kHz mono: the audio
                    with only the voice of the sample) and extracted_vector.
                    The engine keeps no audio after the call, and no sample
                    on its disk.

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
    """Convert any ffmpeg-readable audio to 16 kHz mono float32 samples.
    16 kHz 16-bit mono WAV (what the clients send) needs no ffmpeg: that
    saves about 30 ms for each short clip."""
    import numpy as np

    if data[:4] == b"RIFF" and data[8:12] == b"WAVE":
        try:
            with wave.open(io.BytesIO(data)) as w:
                if (w.getframerate(), w.getnchannels(), w.getsampwidth(), w.getcomptype()) == (STT_RATE, 1, 2, "NONE"):
                    if w.getnframes() > MAX_SECONDS * STT_RATE:
                        raise Fail(413, f"audio is longer than {MAX_SECONDS} seconds")
                    if w.getnframes() == 0:
                        raise Fail(400, "audio has no samples")
                    return np.frombuffer(w.readframes(w.getnframes()), dtype="<i2").astype(np.float32) / 32768
        except (wave.Error, EOFError):
            pass  # ffmpeg can read more forms of WAV
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


MAX_TURN_ITEMS = 32
VAD_CHUNK = 512  # samples at 16 kHz: 32 ms


def norm_words(s):
    """Lower case letters, digits, and apostrophes: speech-to-text gives
    casing and punctuation that the classifier does not need."""
    import re
    s = s.lower().replace("\u2019", "'")
    return " ".join(re.sub(r"[^a-z0-9' ]+", " ", s).split())


def load_turn(directory, threads):
    """Silero VAD (speech segments in short clips) and the turn classifier
    (turn/: is speech over the persona a backchannel, a turn, or too short
    to tell). Both run on the CPU."""
    import numpy as np
    import onnxruntime as ort
    from tokenizers import Tokenizer

    opts = ort.SessionOptions()
    opts.intra_op_num_threads = threads
    opts.inter_op_num_threads = 1
    vad_sess = ort.InferenceSession(str(directory / "silero_vad.onnx"), opts, providers=["CPUExecutionProvider"])
    cfg = json.loads((directory / "turn" / "config.json").read_text())
    tok = Tokenizer.from_file(str(directory / "turn" / "tokenizer.json"))
    tok.enable_truncation(max_length=cfg["max_length"], strategy="only_second")
    tok.no_padding()
    sess = ort.InferenceSession(str(directory / "turn" / "model.onnx"), opts, providers=["CPUExecutionProvider"])
    inputs = cfg["inputs"]
    labels = cfg["labels"]

    def vad(samples):
        """Speech probability for each 32 ms chunk, then segments with
        hysteresis: speech starts at 0.5 and ends after 100 ms under 0.35.
        A segment shorter than 90 ms is noise."""
        state = np.zeros((2, 1, 128), dtype=np.float32)
        ctx = np.zeros(64, dtype=np.float32)
        sr = np.array(STT_RATE, dtype=np.int64)
        probs = []
        for i in range(0, samples.size - VAD_CHUNK + 1, VAD_CHUNK):
            chunk = samples[i:i + VAD_CHUNK]
            out, state = vad_sess.run(None, {"input": np.concatenate([ctx, chunk])[None, :], "state": state, "sr": sr})
            ctx = chunk[-64:]
            probs.append(float(out[0][0]))
        ms = VAD_CHUNK * 1000 // STT_RATE
        segments, start, low = [], None, 0
        for i, p in enumerate(probs):
            if start is None:
                if p >= 0.5:
                    start, low = i, 0
            elif p < 0.35:
                low += 1
                if low * ms >= 100:
                    segments.append([start * ms, (i - low + 1) * ms])
                    start = None
            else:
                low = 0
        if start is not None:
            segments.append([start * ms, (len(probs) - low) * ms])
        segments = [s for s in segments if s[1] - s[0] >= 90]
        return {"speech_ms": sum(e - b for b, e in segments), "segments": segments}

    def classify(items):
        encs = [tok.encode(norm_words(it["text"]), norm_words(it["context"])) for it in items]
        n = max(len(e.ids) for e in encs)
        feed = {k: np.zeros((len(encs), n), dtype=np.int64) for k in inputs}
        for j, e in enumerate(encs):
            k = len(e.ids)
            feed["input_ids"][j, :k] = e.ids
            feed["attention_mask"][j, :k] = 1
            if "token_type_ids" in feed:
                feed["token_type_ids"][j, :k] = e.type_ids
        logits = sess.run(None, feed)[0]
        e = np.exp(logits - logits.max(axis=1, keepdims=True))
        p = e / e.sum(axis=1, keepdims=True)
        return [{labels[c]: round(float(row[c]), 4) for c in range(len(labels))} for row in p]

    return {"vad": vad, "classify": classify}


SPEAKER_MODEL = "3dspeaker-eres2net-en-voxceleb-16k"
SPEAKER_FILE = "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx"
# The speaker model reads at most this much of a clip. A turn is seldom
# longer, and the time of the model grows with the length.
SPEAKER_SECONDS = 30
# A print from less sound than this is not reliable: the engine gives none.
SPEAKER_MIN_SECONDS = 0.4
# VoxLock ignores a voice sample that is shorter than 3 seconds, and then
# it keeps the loudest voice of the audio, which can be a different person.
# Thus there is no extraction with a shorter sample. The engine counts the
# samples of the file that it gives to VoxLock.
SAMPLE_MIN_SECONDS = 3
MAX_SAMPLE = 2 << 20
# The extraction model is built for one voice sample, so one object is kept
# for each of the last people. One object uses about 280 MB of memory.
EXTRACTORS = 2


def trim_sound(samples):
    """Drops the quiet parts at the start and the end of a clip (20 ms
    frames under a tenth of the loudest frame), so a short clip is not
    mostly silence for the speaker model."""
    import numpy as np

    n = 320
    frames = samples[:samples.size - samples.size % n].reshape(-1, n)
    if frames.shape[0] == 0:
        return samples[:0]
    energy = np.sqrt((frames.astype(np.float64) ** 2).mean(axis=1))
    loud = np.nonzero(energy > max(0.1 * energy.max(), 1e-4))[0]
    if loud.size == 0:
        return samples[:0]
    return samples[loud[0] * n:(loud[-1] + 1) * n]


def wav16(samples):
    """A 16-bit mono WAV file at 16 kHz."""
    import numpy as np

    pcm = np.round(np.clip(samples, -1, 1 - 1 / 32768) * 32768).astype("<i2")
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(STT_RATE)
        w.writeframes(pcm.tobytes())
    return buf.getvalue()


def load_speaker(directory, threads):
    """Voice prints with the 3D-Speaker ERes2Net model, and target speaker
    extraction with VoxLock when its files are in directory/voxlock. Both
    run on the CPU with sherpa-onnx. The extraction keeps the voice of one
    person (the sample) and removes the other voices; it can also damage
    the speech, so the caller compares the print of the result again."""
    import collections
    import tempfile

    import numpy as np
    import sherpa_onnx

    ex = sherpa_onnx.SpeakerEmbeddingExtractor(sherpa_onnx.SpeakerEmbeddingExtractorConfig(
        model=str(directory / SPEAKER_FILE), num_threads=threads, provider="cpu"))
    vox = directory / "voxlock"
    chunk = next(iter(sorted(vox.glob("chunk*.onnx"))), None) if vox.is_dir() else None
    extractors = collections.OrderedDict()

    def embed(samples):
        samples = trim_sound(samples)[:SPEAKER_SECONDS * STT_RATE]
        if samples.size < SPEAKER_MIN_SECONDS * STT_RATE:
            return None
        stream = ex.create_stream()
        stream.accept_waveform(STT_RATE, samples)
        stream.input_finished()
        v = np.array(ex.compute(stream), dtype=np.float32)
        norm = float(np.linalg.norm(v))
        if not np.isfinite(norm) or norm == 0:
            return None
        return (v / norm).astype("<f4")

    def extractor(sample):
        """The extraction object for the voice in sample (WAV bytes). The
        file with the sample is there only while the object is made."""
        key = hashlib.sha256(sample).hexdigest()
        if key in extractors:
            extractors.move_to_end(key)
            return extractors[key]
        with tempfile.TemporaryDirectory(prefix="magi-speaker-") as tmp:
            path = Path(tmp) / "sample.wav"
            path.write_bytes(sample)
            mc = sherpa_onnx.OfflineSpeechDenoiserModelConfig(num_threads=threads, provider="cpu")
            mc.voxlock = sherpa_onnx.OfflineSpeechDenoiserVoxLockModelConfig(
                enroll_model=str(vox / "enroll.onnx"), chunk_model=str(chunk), voice_sample=str(path))
            d = sherpa_onnx.OfflineSpeechDenoiser(sherpa_onnx.OfflineSpeechDenoiserConfig(model=mc))
        extractors[key] = d
        while len(extractors) > EXTRACTORS:
            extractors.popitem(last=False)
        return d

    def extract(samples, sample):
        out = extractor(sample).run(samples, STT_RATE)
        x = np.array(out.samples, dtype=np.float32)
        if out.sample_rate != STT_RATE and x.size:
            at = np.arange(0, x.size, out.sample_rate / STT_RATE)
            x = np.interp(at, np.arange(x.size), x).astype(np.float32)
        return x

    def run(samples, sample):
        """sample: 16 kHz samples of the speech of one person, or None."""
        res = {"vector": embed(samples), "extracted": None, "extracted_vector": None}
        if (sample is not None and chunk is not None and sample.size >= SAMPLE_MIN_SECONDS * STT_RATE
                and samples.size >= SPEAKER_MIN_SECONDS * STT_RATE):
            x = extract(samples, wav16(sample[:SPEAKER_SECONDS * STT_RATE]))
            if x.size and np.isfinite(x).all():
                res["extracted"], res["extracted_vector"] = x, embed(x)
        return res

    run.extraction = chunk is not None
    return run


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
        if route == ("POST", "/vad", "turn"):
            samples = decode_audio(self.body(MAX_AUDIO))
            with s.lock:
                start = time.monotonic()
                out = s.run["vad"](samples)
                elapsed = int((time.monotonic() - start) * 1000)
            return self.reply(200, dict(out, audio_ms=samples.size * 1000 // STT_RATE, engine=s.engine_name,
                                        elapsed_ms=elapsed))
        if route == ("POST", "/turn", "turn"):
            try:
                req = json.loads(self.body(256 << 10))
                items = [{"context": str(it.get("context") or ""), "text": str(it["text"])} for it in req["items"]]
            except (ValueError, TypeError, KeyError, AttributeError):
                raise Fail(400, "invalid JSON request")
            if not 1 <= len(items) <= MAX_TURN_ITEMS or any(len(it["text"]) + len(it["context"]) > 4000 for it in items):
                raise Fail(400, f"items must be 1 to {MAX_TURN_ITEMS} items of at most 4000 characters")
            with s.lock:
                start = time.monotonic()
                results = s.run["classify"](items)
                elapsed = int((time.monotonic() - start) * 1000)
            return self.reply(200, {"results": results, "engine": s.engine_name, "elapsed_ms": elapsed})
        if route == ("POST", "/speaker", "speaker"):
            try:
                req = json.loads(self.body((MAX_AUDIO + MAX_SAMPLE) * 4 // 3 + (64 << 10)))
                audio = base64.b64decode(req["audio"], validate=True)
                sample = base64.b64decode(req["sample"], validate=True) if req.get("sample") else None
            except (ValueError, TypeError, KeyError, AttributeError):
                raise Fail(400, "invalid JSON request")
            if not audio or len(audio) > MAX_AUDIO or (sample is not None and len(sample) > MAX_SAMPLE):
                raise Fail(413, "audio or sample is too large")
            samples = decode_audio(audio)
            voice = decode_audio(sample) if sample is not None else None
            with s.lock:
                start = time.monotonic()
                res = s.run(samples, voice)
                elapsed = int((time.monotonic() - start) * 1000)

            def b64(v):
                return base64.b64encode(v.tobytes()).decode() if v is not None else None
            out = {"vector": b64(res["vector"]), "model": SPEAKER_MODEL, "engine": s.engine_name,
                   "audio_ms": samples.size * 1000 // STT_RATE, "elapsed_ms": elapsed}
            if res["extracted"] is not None:
                out["extracted"] = base64.b64encode(wav16(res["extracted"])).decode()
                out["extracted_vector"] = b64(res["extracted_vector"])
            return self.reply(200, out)
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
    ap.add_argument("--engine", choices=["stt", "tts", "detect", "turn", "speaker"], required=True)
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
    elif a.engine == "turn":
        verify(directory, pins["silero-vad"]["sha256"])
        # A turn model that provision.py trained on this host is not the
        # published build: built.json has its hashes and its name.
        built = directory / "turn" / "built.json"
        turn = json.loads(built.read_text()) if built.exists() else pins["turn"]
        verify(directory, {"turn/" + k: v for k, v in turn["sha256"].items()})
        run, name, device = load_turn(directory, a.threads), "silero-vad-5+" + turn["name"], "cpu"
        run["classify"]([{"context": "ready", "text": "okay"}])  # warm up
        run["vad"](__import__("numpy").zeros(STT_RATE, dtype="float32"))
    elif a.engine == "speaker":
        verify(directory, pins["speaker"]["sha256"])
        # The extraction is optional: a host with no VoxLock files gives
        # voice prints only.
        if (directory / "voxlock").is_dir():
            verify(directory, {"voxlock/" + k: v for k, v in pins["voxlock"]["sha256"].items()})
        run, device = load_speaker(directory, a.threads), "cpu"
        name = SPEAKER_MODEL + ("+voxlock" if run.extraction else "")
        np = __import__("numpy")
        tone = (np.sin(np.arange(4 * STT_RATE, dtype="float32") / 20) * 0.1).astype("float32")
        run(tone[:STT_RATE], tone if run.extraction else None)  # warm up
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
