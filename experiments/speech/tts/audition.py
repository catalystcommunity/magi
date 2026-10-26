#!/usr/bin/env python3
"""Prepare pinned models or measure local TTS adapters on fictional prompts."""

import argparse
from datetime import datetime, timezone
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import platform
import random
import re
import resource
import signal
import sys
import time
import wave


MODELS = {
    "kokoro": {
        "repo": "hexgrad/Kokoro-82M",
        "revision": "f3ff3571791e39611d31c381e3a41a3af07b4987",
        "license": "Apache-2.0",
        "voices": ["af_heart", "af_bella", "af_nicole", "af_sarah", "af_sky",
                   "am_fenrir", "am_michael", "am_puck", "bf_emma",
                   "bf_isabella", "bm_george", "bm_fable"],
    },
    "qwen": {
        "repo": "Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice",
        "revision": "0c0e3051f131929182e2c023b9537f8b1c68adfe",
        "license": "Apache-2.0",
        "voices": ["ryan", "aiden", "vivian", "serena", "uncle_fu", "dylan",
                   "eric", "ono_anna", "sohee"],
    },
}


def digest(path):
    value = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            value.update(chunk)
    return value.hexdigest()


def save_json(path, value):
    path = Path(path)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, allow_nan=False) + "\n")
    temporary.replace(path)


def prepare(root, engines):
    from huggingface_hub import snapshot_download

    root.mkdir(parents=True, exist_ok=True)
    for engine in engines:
        spec = MODELS[engine]
        patterns = ["*.json", "*.safetensors", "*.txt", "README.md"]
        if engine == "kokoro":
            patterns = ["config.json", "kokoro-v1_0.pth", "README.md", "VOICES.md"]
            patterns += [f"voices/{voice}.pt" for voice in spec["voices"]]
        destination = root / engine
        snapshot_download(repo_id=spec["repo"], revision=spec["revision"],
                          local_dir=destination, allow_patterns=patterns,
                          max_workers=2, token=False)
        files = {str(p.relative_to(destination)): digest(p)
                 for p in sorted(destination.rglob("*"))
                 if p.is_file() and ".cache" not in p.parts}
        save_json(root / f"{engine}-provenance.json", {**spec, "sha256": files})
        print(json.dumps({"prepared": engine, "files": len(files)}), flush=True)


class Kokoro:
    delivery = "phoneme-bounded text chunks; not incremental audio within a chunk"

    def __init__(self, model_dir):
        from kokoro import KModel, KPipeline
        self.directory = model_dir
        model = KModel(repo_id=MODELS["kokoro"]["repo"],
                       config=str(model_dir / "config.json"),
                       model=str(model_dir / "kokoro-v1_0.pth")).to("cpu").eval()
        self.pipelines = {
            lang: KPipeline(lang_code=lang, repo_id=MODELS["kokoro"]["repo"],
                            model=model, device="cpu") for lang in ("a", "b")
        }
        # Load each profile before timing synthesis. Do not fetch moving HF refs.
        for voice in MODELS["kokoro"]["voices"]:
            self.pipelines[voice[0]].load_single_voice(str(model_dir / "voices" / f"{voice}.pt"))

    def generate(self, text, voice):
        path = str(self.directory / "voices" / f"{voice}.pt")
        for result in self.pipelines[voice[0]](text, voice=path, speed=1.0):
            if result.audio is not None:
                yield result.audio.detach().cpu().numpy(), 24000


class Qwen:
    delivery = "complete utterance; the upstream Python API does not yield audio chunks"

    def __init__(self, model_dir):
        import torch
        from qwen_tts import Qwen3TTSModel
        self.model = Qwen3TTSModel.from_pretrained(
            str(model_dir), device_map="cpu", dtype=torch.float32,
            attn_implementation="eager", local_files_only=True,
        )

    def generate(self, text, voice):
        wavs, rate = self.model.generate_custom_voice(
            text=text, language="English", speaker=voice, instruct="",
            non_streaming_mode=True, max_new_tokens=1024,
        )
        for samples in wavs:
            yield samples, rate


def encode_audio(chunks, sample_rate, path):
    import numpy as np

    if not chunks or sample_rate <= 0:
        raise ValueError("The engine returned no audio.")
    arrays = [np.asarray(chunk, dtype=np.float32) for chunk in chunks]
    if any(a.ndim != 1 for a in arrays):
        raise ValueError("The engine must return mono samples.")
    samples = np.concatenate(arrays)
    if not samples.size or not np.isfinite(samples).all():
        raise ValueError("The audio is empty or has non-finite samples.")
    if samples.size / sample_rate > 120:
        raise ValueError("The audio exceeds the 120 second fixture limit.")
    peak = float(np.max(np.abs(samples)))
    clipped = int(np.count_nonzero(np.abs(samples) >= 1))
    rms = float(np.sqrt(np.mean(samples.astype(np.float64) ** 2)))
    # Preserve engine amplitude. Clip only at PCM limits. Do not normalize samples.
    pcm = np.round(np.clip(samples, -1, 1 - 1 / 32768) * 32768).astype("<i2")
    with wave.open(str(path), "wb") as target:
        target.setnchannels(1)
        target.setsampwidth(2)
        target.setframerate(sample_rate)
        target.writeframes(pcm.tobytes())
    return {"audio_seconds": samples.size / sample_rate, "sample_rate": sample_rate,
            "frames": int(samples.size), "peak": peak, "rms": rms,
            "clipped_samples": clipped, "sha256": digest(path)}


def measure(adapter, text, voice, path):
    chunks, events = [], []
    sample_rate = None
    started = time.perf_counter()
    for chunk, rate in adapter.generate(text, voice):
        elapsed = time.perf_counter() - started
        if sample_rate is not None and rate != sample_rate:
            raise ValueError("The sample rate changed within the utterance.")
        sample_rate = rate
        chunks.append(chunk)
        events.append({"ready_seconds": elapsed, "frames": len(chunk)})
    generation = time.perf_counter() - started
    audio = encode_audio(chunks, sample_rate or 0, path)
    return {**audio, "generation_seconds": generation,
            "first_audio_seconds": events[0]["ready_seconds"],
            "real_time_factor": generation / audio["audio_seconds"], "chunks": events}


def read_prompts(path):
    document = json.loads(path.read_text())
    prompts = document["prompts"]
    identifiers = [p["id"] for p in prompts]
    if len(set(identifiers)) != len(identifiers):
        raise ValueError("Prompt IDs must be unique.")
    for p in prompts:
        if not re.fullmatch(r"[a-z0-9_-]+", p["id"]):
            raise ValueError("Prompt IDs must be safe filenames.")
        if not isinstance(p["text"], str) or not 1 <= len(p["text"]) <= 2000:
            raise ValueError("Each prompt must contain 1 to 2000 characters.")
    return prompts


def run(args):
    import numpy as np
    import torch

    spec = MODELS[args.engine]
    prompts = read_prompts(args.prompts)
    if args.prompt_ids:
        selected = set(args.prompt_ids)
        if selected - {p["id"] for p in prompts}:
            raise ValueError("Unknown prompt ID.")
        prompts = [p for p in prompts if p["id"] in selected]
    voices = args.voices or spec["voices"]
    if set(voices) - set(spec["voices"]) or len(voices) != len(set(voices)):
        raise ValueError("Select unique stock voice IDs from the pinned catalogue.")
    provenance_path = args.models / f"{args.engine}-provenance.json"
    provenance = json.loads(provenance_path.read_text())
    if provenance["revision"] != spec["revision"]:
        raise ValueError("The prepared model revision does not match this test.")
    for name, expected in provenance["sha256"].items():
        if digest(args.models / args.engine / name) != expected:
            raise ValueError(f"Model file changed: {name}")
    # Prevent silent overwrite of previous evidence, even after a partial run.
    args.output.mkdir(parents=True, exist_ok=False)
    torch.set_num_threads(args.threads)
    torch.set_num_interop_threads(1)
    report = {
        "schema_version": 1, "status": "running", "engine": args.engine,
        "started_at": datetime.now(timezone.utc).isoformat(), "model": provenance,
        "host": platform.node(), "platform": platform.platform(),
        "python": platform.python_version(), "device": "cpu", "dtype": "float32",
        "threads": args.threads, "interop_threads": 1, "seed": args.seed,
        "prompt_sha256": digest(args.prompts), "prompts": prompts, "voices": voices,
        "repeats": args.repeats, "warmup": None, "samples": [],
        "packages": {d.metadata["Name"]: d.version for d in importlib.metadata.distributions()},
        "scope": "Serial offline synthesis; excludes transport, playback and cold-host startup. No voice quality score.",
        "settings": {"speed": 1.0} if args.engine == "kokoro" else {
            "language": "English", "instruct": "", "non_streaming_mode": True,
            "attention": "eager", "max_new_tokens": 1024,
            "sampling": "upstream pinned generation_config.json defaults",
        },
    }
    manifest = args.output / "manifest.json"
    save_json(manifest, report)
    def interrupted(signum, _frame):
        raise InterruptedError(f"Received signal {signum}")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        start = time.perf_counter()
        adapter = {"kokoro": Kokoro, "qwen": Qwen}[args.engine](args.models / args.engine)
        report["model_load_seconds"] = time.perf_counter() - start
        report["delivery"] = adapter.delivery
        # A separate warmup record makes first-use work visible without mixing it
        # into the steady-state comparison. Keep the warmup waveform as evidence.
        torch.manual_seed(args.seed)
        np.random.seed(args.seed)
        random.seed(args.seed)
        report["warmup"] = measure(adapter, "This is a speech test.", voices[0], args.output / "warmup.wav")
        save_json(manifest, report)
        for prompt in prompts:
            for voice in voices:
                for repeat in range(args.repeats):
                    torch.manual_seed(args.seed + repeat)
                    np.random.seed(args.seed + repeat)
                    random.seed(args.seed + repeat)
                    name = f"{voice}--{prompt['id']}--{repeat + 1}.wav"
                    record = {"voice": voice, "prompt": prompt["id"], "repeat": repeat + 1,
                              "seed": args.seed + repeat, "file": name}
                    record.update(measure(adapter, prompt["text"], voice, args.output / name))
                    report["samples"].append(record)
                    report["peak_rss_kib"] = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
                    save_json(manifest, report)
                    print(json.dumps({"engine": args.engine, **record}), flush=True)
        report["status"] = "complete"
    except BaseException as error:
        report["status"] = "interrupted" if isinstance(error, (InterruptedError, KeyboardInterrupt)) else "failed"
        report["error"] = f"{type(error).__name__}: {error}"
        raise
    finally:
        report["finished_at"] = datetime.now(timezone.utc).isoformat()
        report["peak_rss_kib"] = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
        save_json(manifest, report)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare_parser = commands.add_parser("prepare")
    prepare_parser.add_argument("--models", type=Path, required=True)
    prepare_parser.add_argument("--engines", nargs="+", choices=MODELS, default=list(MODELS))
    runner = commands.add_parser("run")
    runner.add_argument("--engine", choices=MODELS, required=True)
    runner.add_argument("--models", type=Path, required=True)
    runner.add_argument("--output", type=Path, required=True)
    runner.add_argument("--prompts", type=Path, default=Path(__file__).with_name("prompts.json"))
    runner.add_argument("--prompt-ids", nargs="+")
    runner.add_argument("--voices", nargs="+")
    runner.add_argument("--threads", type=int, choices=range(1, 9), default=4)
    runner.add_argument("--repeats", type=int, choices=range(1, 6), default=1)
    runner.add_argument("--seed", type=int, default=1729)
    args = parser.parse_args()
    if args.command == "prepare":
        prepare(args.models, args.engines)
    else:
        # Set these before torch or NumPy imports. Do not use account-level caches.
        os.environ["OMP_NUM_THREADS"] = str(args.threads)
        os.environ["OPENBLAS_NUM_THREADS"] = str(args.threads)
        os.environ["HF_HUB_OFFLINE"] = "1"
        os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"
        run(args)


if __name__ == "__main__":
    main()
