#!/usr/bin/env python3
"""Verify audition evidence and produce a summary from measured samples."""

import argparse
import json
import math
from pathlib import Path
import statistics
import wave

from audition import digest, save_json


def verify(root):
    summaries = []
    for path in sorted(root.glob("*/manifest.json")):
        record = json.loads(path.read_text())
        if record["status"] != "complete":
            raise ValueError(f"Incomplete run: {path}")
        expected = {(voice, prompt["id"], repeat)
                    for voice in record["voices"] for prompt in record["prompts"]
                    for repeat in range(1, record["repeats"] + 1)}
        actual = [(sample["voice"], sample["prompt"], sample["repeat"])
                  for sample in record["samples"]]
        if len(actual) != len(set(actual)) or set(actual) != expected:
            raise ValueError(f"Missing or duplicate samples: {path}")
        for sample in record["samples"]:
            target = (path.parent / sample["file"]).resolve()
            if target.parent != path.parent.resolve() or digest(target) != sample["sha256"]:
                raise ValueError(f"Audio location or hash mismatch: {sample['file']}")
            with wave.open(str(target)) as audio:
                if (audio.getsampwidth(), audio.getnchannels(), audio.getframerate(), audio.getnframes()) != (
                        2, 1, sample["sample_rate"], sample["frames"]):
                    raise ValueError(f"Audio format mismatch: {target}")
                if not math.isclose(audio.getnframes() / audio.getframerate(), sample["audio_seconds"]):
                    raise ValueError(f"Audio duration mismatch: {target}")
                if len(audio.readframes(audio.getnframes())) != sample["frames"] * 2:
                    raise ValueError(f"Truncated WAV: {target}")
            if not 0 < sample["first_audio_seconds"] <= sample["generation_seconds"]:
                raise ValueError(f"Invalid timing: {target}")
            if sum(chunk["frames"] for chunk in sample["chunks"]) != sample["frames"]:
                raise ValueError(f"Chunk size mismatch: {target}")
            if not math.isclose(sample["real_time_factor"], sample["generation_seconds"] / sample["audio_seconds"]):
                raise ValueError(f"Invalid real-time factor: {target}")
        groups = {}
        for prompt in record["prompts"]:
            samples = [s for s in record["samples"] if s["prompt"] == prompt["id"]]
            groups[prompt["id"]] = {
                "samples": len(samples),
                "first_audio_min_seconds": min(s["first_audio_seconds"] for s in samples),
                "first_audio_max_seconds": max(s["first_audio_seconds"] for s in samples),
                "median_real_time_factor": statistics.median(s["real_time_factor"] for s in samples),
                "generation_total_seconds": sum(s["generation_seconds"] for s in samples),
                "audio_total_seconds": sum(s["audio_seconds"] for s in samples),
            }
        summaries.append({"run": path.parent.name, "engine": record["engine"],
                          "samples": len(record["samples"]), "voices": len(record["voices"]),
                          "model_load_seconds": record["model_load_seconds"],
                          "peak_rss_mib": record["peak_rss_kib"] / 1024,
                          "clipped_samples": sum(s["clipped_samples"] for s in record["samples"]),
                          "prompts": groups, "manifest_sha256": digest(path)})
    if not summaries:
        raise ValueError("No manifests found.")
    return {"scope": "Observed ranges, not percentiles or quality scores.", "runs": summaries}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results", type=Path, required=True)
    args = parser.parse_args()
    summary = verify(args.results)
    save_json(args.results / "summary.json", summary)
    print(json.dumps(summary, indent=2))
