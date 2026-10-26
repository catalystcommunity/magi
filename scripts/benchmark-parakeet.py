#!/usr/bin/env python3
"""Measure offline Parakeet CPU inference on an explicit public audio fixture."""

import argparse
import hashlib
import importlib.metadata
import json
from pathlib import Path
import platform
import resource
import time
import wave


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model-dir", type=Path, required=True)
    parser.add_argument("--wav", type=Path, required=True)
    parser.add_argument("--threads", type=int, default=2, choices=range(1, 9))
    parser.add_argument("--runs", type=int, default=3, choices=range(1, 11))
    args = parser.parse_args()

    import numpy as np
    import sherpa_onnx

    with wave.open(str(args.wav), "rb") as audio:
        if audio.getnchannels() != 1 or audio.getsampwidth() != 2:
            parser.error("The fixture must be mono signed 16-bit PCM WAV.")
        rate = audio.getframerate()
        frames = audio.getnframes()
        if rate <= 0 or frames <= 0 or frames / rate > 120:
            parser.error("The fixture must contain 0 to 120 seconds of audio.")
        samples = np.frombuffer(audio.readframes(frames), dtype="<i2")
    samples = samples.astype(np.float32) / 32768.0
    duration = frames / rate
    files = {
        "encoder": args.model_dir / "encoder.int8.onnx",
        "decoder": args.model_dir / "decoder.int8.onnx",
        "joiner": args.model_dir / "joiner.int8.onnx",
        "tokens": args.model_dir / "tokens.txt",
    }
    started = time.perf_counter()
    recognizer = sherpa_onnx.OfflineRecognizer.from_transducer(
        **{key: str(path) for key, path in files.items()},
        num_threads=args.threads,
        sample_rate=16000,
        feature_dim=80,
        decoding_method="greedy_search",
        model_type="nemo_transducer",
        provider="cpu",
        debug=False,
    )
    load_seconds = time.perf_counter() - started
    results = []
    for index in range(args.runs):
        started = time.perf_counter()
        stream = recognizer.create_stream()
        stream.accept_waveform(rate, samples)
        recognizer.decode_stream(stream)
        elapsed = time.perf_counter() - started
        results.append({
            "run": index + 1,
            "decode_seconds": elapsed,
            "real_time_factor": elapsed / duration,
            "text": stream.result.text,
        })
    # Linux reports ru_maxrss in KiB. Other systems can use different units.
    peak_rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    report = {
        "scope": "offline single public fixture; not household accuracy or service latency",
        "host": platform.node(),
        "platform": platform.platform(),
        "python": platform.python_version(),
        "sherpa_onnx": importlib.metadata.version("sherpa-onnx"),
        "sherpa_onnx_core": importlib.metadata.version("sherpa-onnx-core"),
        "numpy": np.__version__,
        "provider": "cpu",
        "threads": args.threads,
        "audio_seconds": duration,
        "sample_rate": rate,
        "feature_sample_rate": 16000,
        "audio_sha256": digest(args.wav),
        "model_load_seconds": load_seconds,
        "peak_rss_kib": peak_rss if platform.system() == "Linux" else None,
        "runs": results,
        "model_sha256": {key: digest(path) for key, path in files.items()},
    }
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
