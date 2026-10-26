#!/usr/bin/env python3
"""Measure process cancellation after warmup. This is not service cancellation."""

import argparse
import json
from pathlib import Path
import subprocess
import sys
import time

from audition import digest, save_json


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--engine", choices=("kokoro", "qwen"), required=True)
    parser.add_argument("--models", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    runner = Path(__file__).with_name("audition.py")
    command = [sys.executable, str(runner), "run", "--engine", args.engine,
               "--models", str(args.models.resolve()), "--output", str(args.output / "run"),
               "--prompt-ids", "long", "--voices", "af_heart" if args.engine == "kokoro" else "ryan",
               "--threads", "4"]
    report = {"engine": args.engine, "scope": "SIGTERM of one experiment process during a post-warmup request. Not API cancellation or stale-playback suppression.",
              "runner_sha256": digest(runner)}
    manifest = args.output / "run" / "manifest.json"
    process = None
    try:
        with (args.output / "process.log").open("wb") as output:
            process = subprocess.Popen(command, stdout=output, stderr=subprocess.STDOUT)
            deadline = time.monotonic() + 180
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError("The process exited before the cancellation probe.")
                if manifest.is_file() and json.loads(manifest.read_text()).get("warmup"):
                    # The runner has started its next request after the atomic save.
                    time.sleep(.25)
                    break
                time.sleep(.1)
            else:
                raise TimeoutError("Warmup did not complete within 180 seconds.")
            before = json.loads(manifest.read_text())
            if before["status"] != "running" or before["samples"]:
                raise RuntimeError("The probe missed the active request window.")
            started = time.perf_counter()
            process.terminate()
            process.wait(timeout=10)
            report["signal_to_exit_seconds"] = time.perf_counter() - started
            after = json.loads(manifest.read_text())
            report.update({"exit_code": process.returncode, "status": after["status"],
                           "error": after.get("error"), "completed_samples": len(after["samples"]),
                           "manifest_sha256": digest(manifest)})
            report["passed"] = after["status"] == "interrupted" and not after["samples"]
            if not report["passed"]:
                raise RuntimeError("The process did not retain the expected interrupted state.")
    except BaseException as error:
        report["passed"] = False
        report["probe_error"] = f"{type(error).__name__}: {error}"
        raise
    finally:
        if process is not None and process.poll() is None:
            process.kill()
            process.wait()
        save_json(args.output / "result.json", report)
    print(json.dumps(report))


if __name__ == "__main__":
    main()
