#!/usr/bin/env python3
"""Read audition audio with local Parakeet. This is not a voice quality score."""

import argparse
import json
from pathlib import Path
import platform
import time
import wave

from audition import digest, save_json


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model-dir", type=Path, required=True)
    parser.add_argument("--results", type=Path, required=True)
    args = parser.parse_args()
    import numpy as np
    import sherpa_onnx

    files = {"encoder": "encoder.int8.onnx", "decoder": "decoder.int8.onnx",
             "joiner": "joiner.int8.onnx", "tokens": "tokens.txt"}
    recognizer = sherpa_onnx.OfflineRecognizer.from_transducer(
        **{key: str(args.model_dir / name) for key, name in files.items()},
        num_threads=2, sample_rate=16000, feature_dim=80,
        decoding_method="greedy_search", model_type="nemo_transducer", provider="cpu",
    )
    report = {"scope": "One ASR engine reading synthetic speech. Disagreement can be a TTS or ASR error. This is not WER or a human voice quality score.",
              "model": "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8",
              "runtime": sherpa_onnx.__version__, "host": platform.node(),
              "model_sha256": {key: digest(args.model_dir / name) for key, name in files.items()},
              "samples": []}
    for manifest in sorted(args.results.glob("*/manifest.json")):
        source = json.loads(manifest.read_text())
        prompts = {p["id"]: p["text"] for p in source["prompts"]}
        for sample in source["samples"]:
            path = manifest.parent / sample["file"]
            if digest(path) != sample["sha256"]:
                raise ValueError(f"Audio hash mismatch: {path}")
            with wave.open(str(path)) as audio:
                if audio.getsampwidth() != 2 or audio.getnchannels() != 1:
                    raise ValueError("Expected mono 16-bit PCM.")
                rate, frames = audio.getframerate(), audio.getnframes()
                if not 0 < frames / rate <= 120:
                    raise ValueError("Audio duration is outside fixture limits.")
                pcm = np.frombuffer(audio.readframes(frames), dtype="<i2").astype(np.float32) / 32768
            start = time.perf_counter()
            stream = recognizer.create_stream()
            stream.accept_waveform(rate, pcm)
            recognizer.decode_stream(stream)
            report["samples"].append({"run": manifest.parent.name, "file": sample["file"],
                                      "sha256": sample["sha256"], "input_text": prompts[sample["prompt"]],
                                      "transcript": stream.result.text,
                                      "decode_seconds": time.perf_counter() - start})
            save_json(args.results / "transcripts.json", report)
    print(json.dumps({"transcribed": len(report["samples"])}))


if __name__ == "__main__":
    main()
