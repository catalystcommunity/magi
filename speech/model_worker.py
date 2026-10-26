"""Local inference worker. The parent supplies fixed configuration and bounded PCM."""

import base64
import hashlib
import json
import os
from pathlib import Path
import sys


def verify_files(directory, pins):
    for name, expected in pins["sha256"].items():
        source = directory / name
        digest = hashlib.sha256()
        with source.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
        if digest.hexdigest() != expected:
            raise ValueError("Model file does not match the pin.")


def load(config):
    import numpy as np

    kind = config["kind"]
    directory = Path(config["model_dir"])
    if kind == "tts":
        import torch
        from kokoro import KModel, KPipeline

        pins = config["pins"]["kokoro"]
        verify_files(directory, pins)
        torch.set_num_threads(4)
        torch.set_num_interop_threads(1)
        model = KModel(repo_id=pins["repo"], config=str(directory / "config.json"),
                       model=str(directory / "kokoro-v1_0.pth")).to("cpu").eval()
        pipelines = {lang: KPipeline(lang_code=lang, repo_id=pins["repo"], model=model,
                                    device="cpu") for lang in ("a", "b")}
        for voice in pins["voices"]:
            pipelines[voice[0]].load_single_voice(str(directory / "voices" / f"{voice}.pt"))

        def process(request):
            voice = request["voice"]
            if voice not in pins["voices"] or not 0 < len(request["text"].encode()) <= 2000:
                raise ValueError("Invalid TTS input.")
            torch.manual_seed(1729)
            arrays, size = [], 0
            for result in pipelines[voice[0]](request["text"],
                    voice=str(directory / "voices" / f"{voice}.pt"), speed=1.0):
                if result.audio is None:
                    continue
                audio = result.audio.detach().cpu().numpy()
                size += audio.size
                if audio.ndim != 1 or not np.isfinite(audio).all() or size > 2880000:
                    raise ValueError("Invalid TTS output.")
                arrays.append(audio)
            if not arrays or size == 0:
                raise ValueError("Empty TTS output.")
            samples = np.concatenate(arrays)
            pcm = np.round(np.clip(samples, -1, 1 - 1 / 32768) * 32768).astype("<i2")
            return {"pcm": base64.b64encode(pcm.tobytes()).decode(), "sample_rate": 24000,
                    "transcript": ""}
        return process
    if kind == "stt":
        import sherpa_onnx

        verify_files(directory, config["pins"]["parakeet"])
        recognizer = sherpa_onnx.OfflineRecognizer.from_transducer(
            encoder=str(directory / "encoder.int8.onnx"),
            decoder=str(directory / "decoder.int8.onnx"),
            joiner=str(directory / "joiner.int8.onnx"), tokens=str(directory / "tokens.txt"),
            num_threads=2, sample_rate=16000, feature_dim=80,
            decoding_method="greedy_search", model_type="nemo_transducer", provider="cpu")

        def process(request):
            pcm = base64.b64decode(request["pcm"], validate=True)
            if not 0 < len(pcm) <= 1920000 or len(pcm) % 2:
                raise ValueError("Invalid STT input.")
            samples = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768
            stream = recognizer.create_stream()
            stream.accept_waveform(16000, samples)
            recognizer.decode_stream(stream)
            text = stream.result.text
            if not text.strip() or len(text.encode()) > 8192:
                raise ValueError("Invalid transcript.")
            return {"pcm": "", "sample_rate": 0, "transcript": text}
        return process
    raise ValueError("Unsupported backend.")


def main():
    # Give protocol output its own descriptor before imports that can print.
    output = os.fdopen(os.dup(sys.stdout.fileno()), "w", buffering=1)
    os.dup2(sys.stderr.fileno(), sys.stdout.fileno())
    config = json.loads(sys.stdin.buffer.readline(65537))
    process = load(config)
    output.write(json.dumps({"ready": True}) + "\n")
    while True:
        line = sys.stdin.buffer.readline(2600001)
        if not line:
            return
        if len(line) > 2600000 or not line.endswith(b"\n"):
            raise ValueError("Invalid frame.")
        try:
            result = process(json.loads(line))
            result["error_code"] = ""
        except Exception:
            # Do not persist library exceptions that can echo input text.
            result = {"error_code": "inference_failed", "pcm": "", "sample_rate": 0, "transcript": ""}
        output.write(json.dumps(result, allow_nan=False) + "\n")


if __name__ == "__main__":
    main()
