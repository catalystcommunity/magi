"""Provision a model engine host: the Python environments and the models of
the engines stt, tts, detect, and turn (primitives/speech/README.md).

  python provision.py [--engines stt,tts,detect,turn] [--torch auto|cpu|cuda|rocm]
                      [--turn-from DIR] [--train-device auto|cpu|cuda]

It runs on the engine host as the person, with no root. install.sh
--provision copies it to the host with the lock files and runs it. It
needs uv (install.sh gets a pinned uv when the host has none) and the
standard library only. It can run again: it skips an environment whose lock
did not change and a model file whose hash is correct.

- Environments: ROOT/envs/{stt,tts,runtime,train} from requirements/*.txt
  (pinned, with hashes). uv gets the Python version that each needs.
- Models: ROOT/models/{parakeet,kokoro,injection,turn}. Each file is
  checked against model-pins.json.
- The turn classifier is Magi's own model. With --turn-from DIR it copies a
  build (DIR has model.onnx or model-int8.onnx, tokenizer.json,
  config.json). Otherwise it trains one on this host with turn/train.py
  (about 10 minutes on a GPU; on a CPU much longer). A build that is not
  the published one gets turn/built.json with its hashes; the engine
  checks the files against that.
- ROOT/engines.env gives install.sh the paths.

ROOT is MAGI_SPEECH_ROOT, by default ~/.local/share/magi/speech.
"""

import argparse
import datetime
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = Path(os.environ.get("MAGI_SPEECH_ROOT", Path.home() / ".local/share/magi/speech"))
TORCH_INDEX = {"cpu": "cpu", "cuda": "cu128", "rocm": "rocm6.4"}
TRAIN_INDEX = {"cpu": "cpu", "cuda": "cu124"}
PARAKEET = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/{revision}.tar.bz2"
HF = "https://huggingface.co/{repo}/resolve/{revision}/{path}"
GITHUB_RAW = "https://raw.githubusercontent.com/{repo}/{revision}/{path}"
# The most turns that a trained model may take as an acknowledgment on the
# held-out set (P(ack) >= 0.9). A persona that goes on after "stop" is the
# worst error; the published build has 0.
MAX_TURNS_AS_ACK = 2


def say(*a):
    print("provision:", *a, flush=True)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def fetch(url, dest, want=None):
    """Download url to dest. With want, the SHA-256 must match; a file that
    is already there with the right hash is not downloaded again."""
    dest = Path(dest)
    if want and dest.exists() and sha256(dest) == want:
        return
    dest.parent.mkdir(parents=True, exist_ok=True)
    say("download", url)
    req = urllib.request.Request(url, headers={"User-Agent": "magi-provision"})
    tmp = dest.with_name(dest.name + ".part")
    with urllib.request.urlopen(req, timeout=60) as r, open(tmp, "wb") as f:
        shutil.copyfileobj(r, f, 1 << 20)
    if want and sha256(tmp) != want:
        tmp.unlink()
        raise SystemExit(f"{url}: the file does not match its pin")
    tmp.replace(dest)


def uv():
    for p in [os.environ.get("UV"), shutil.which("uv"), Path.home() / "magi-speech/bin/uv", Path.home() / ".local/bin/uv"]:
        if p and Path(p).is_file():
            return str(p)
    raise SystemExit("uv is required (install.sh --provision installs a pinned uv)")


def run(*cmd, **kw):
    subprocess.run([str(c) for c in cmd], check=True, **kw)


def environment(name, python, lock, index=None):
    """A virtual environment with exactly the packages of the lock file."""
    env = ROOT / "envs" / name
    lock = HERE / "requirements" / lock
    stamp = env / ".magi-lock"
    want = sha256(lock)
    if stamp.exists() and stamp.read_text().strip() == want and (env / "bin/python").exists():
        return env / "bin/python"
    say(f"environment {name} (Python {python}, {lock.name})")
    if not (env / "bin/python").exists():
        run(uv(), "venv", "--quiet", "--python", python, env)
    extra = ["--extra-index-url", f"https://download.pytorch.org/whl/{index}", "--index-strategy", "unsafe-best-match"] if index else []
    run(uv(), "pip", "sync", "--quiet", "--python", env / "bin/python", *extra, lock)
    stamp.write_text(want + "\n")
    return env / "bin/python"


def torch_build(choice):
    if choice != "auto":
        return choice
    try:
        if subprocess.run(["nvidia-smi", "-L"], capture_output=True, timeout=20).returncode == 0:
            return "cuda"
    except (OSError, subprocess.TimeoutExpired):
        pass
    if Path("/dev/kfd").exists() and (shutil.which("rocminfo") or shutil.which("rocm-smi")):
        return "rocm"
    return "cpu"


def parakeet(pins):
    p = pins["parakeet"]
    out = ROOT / "models/parakeet"
    if all((out / n).exists() and sha256(out / n) == h for n, h in p["sha256"].items()):
        return out
    with tempfile.TemporaryDirectory(dir=ROOT) as tmp:
        archive = Path(tmp) / "parakeet.tar.bz2"
        fetch(PARAKEET.format(revision=p["revision"]), archive)
        with tarfile.open(archive) as t:
            for m in t.getmembers():
                name = Path(m.name).name
                if name in p["sha256"] and m.isfile():
                    m.name = name
                    t.extract(m, out, filter="data")
    for n, h in p["sha256"].items():
        if sha256(out / n) != h:
            raise SystemExit(f"parakeet {n} does not match its pin")
    return out


def hf_files(pins, key, out):
    p = pins[key]
    prefix = p.get("files", "")
    for name, h in p["sha256"].items():
        path = name if name in p.get("root_files", []) else prefix + name
        fetch(HF.format(repo=p["repo"], revision=p["revision"], path=path), out / name, h)
    return out


def turn_model(pins, args, train_build):
    """Silero VAD and the turn classifier in ROOT/models/turn."""
    out = ROOT / "models/turn"
    s = pins["silero-vad"]
    for name, h in s["sha256"].items():
        fetch(GITHUB_RAW.format(repo=s["repo"], revision=s["revision"], path=s["files"] + name), out / name, h)
    lic = out / "LICENSE-silero-vad"
    if not lic.exists():
        fetch(GITHUB_RAW.format(repo=s["repo"], revision=s["revision"], path="LICENSE"), lic)
    t = out / "turn"
    published = pins["turn"]["sha256"]
    built = t / "built.json"
    have = lambda hashes: all((t / n).exists() and sha256(t / n) == h for n, h in hashes.items())
    if have(published):
        built.unlink(missing_ok=True)
        return out
    if built.exists() and have(json.loads(built.read_text())["sha256"]) and not args.turn_from:
        return out
    if args.turn_from:
        src = Path(args.turn_from).expanduser()
        how = f"copied from {src}"
    else:
        src = train(pins, args, train_build)
        how = "trained on this host"
    t.mkdir(parents=True, exist_ok=True)
    model = src / "model-int8.onnx" if (src / "model-int8.onnx").exists() else src / "model.onnx"
    shutil.copyfile(model, t / "model.onnx")
    for n in ["tokenizer.json", "config.json", "report.json"]:
        if (src / n).exists():
            shutil.copyfile(src / n, t / n)
    if have(published):
        built.unlink(missing_ok=True)
        say("turn classifier: the published build", pins["turn"]["name"])
        return out
    hashes = {n: sha256(t / n) for n in published}
    built.write_text(json.dumps({"name": pins["turn"]["name"] + "-local", "how": how,
                                 "made": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
                                 "sha256": hashes}, indent=1) + "\n")
    say("turn classifier:", how, "(turn/built.json)")
    return out


def train(pins, args, build):
    py = environment("train", "3.12", f"train-{build}.txt", TRAIN_INDEX[build])
    work = ROOT / "build/turn"
    work.mkdir(parents=True, exist_ok=True)
    say(f"train the turn classifier, torch build {build} (log: {work}/train.log)")
    env = dict(os.environ, HF_HOME=str(ROOT / "hf"), TOKENIZERS_PARALLELISM="false", PYTHONDONTWRITEBYTECODE="1")
    with open(work / "train.log", "w") as log:
        run(py, "-I", "-u", HERE / "turn/train.py", work, "--device", args.train_device if args.train_device != "auto" else build,
            stdout=log, stderr=subprocess.STDOUT, env=env)
    report = json.loads((work / "report.json").read_text())
    r = report["model-int8.onnx"]["heldout"]["0.9"]
    say(f"held-out: accuracy {r['accuracy']}, turns taken as ack {r['turns_taken_as_ack']} of {r['turns']}")
    if r["turns_taken_as_ack"] > MAX_TURNS_AS_ACK:
        raise SystemExit(f"the trained model takes {r['turns_taken_as_ack']} held-out turns as acknowledgments "
                         f"(at most {MAX_TURNS_AS_ACK}); see {work}/report.json")
    return work


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--engines", default="stt,tts,detect,turn")
    ap.add_argument("--torch", choices=["auto", "cpu", "cuda", "rocm"], default="auto", help="torch build of the TTS engine")
    ap.add_argument("--turn-from", help="a directory with a turn classifier build to copy")
    ap.add_argument("--train-device", choices=["auto", "cpu", "cuda"], default="auto")
    args = ap.parse_args()
    engines = set(args.engines.split(","))
    if engines - {"stt", "tts", "detect", "turn"}:
        raise SystemExit(f"unknown engines: {sorted(engines - {'stt', 'tts', 'detect', 'turn'})}")
    if os.uname().machine != "x86_64":
        say(f"warning: the lock files are made for x86_64, not {os.uname().machine}")
    pins = json.loads((HERE / "model-pins.json").read_text())
    # espeak-ng (Kokoro) keeps its data path in a buffer of 160 bytes; a
    # longer path is cut, and the engine does not start.
    espeak = ROOT / "envs/tts/lib/python3.12/site-packages/espeakng_loader/espeak-ng-data"
    if "tts" in engines and len(str(espeak).encode()) >= 150:
        raise SystemExit(f"MAGI_SPEECH_ROOT is too long for espeak-ng ({len(str(espeak).encode())} bytes in {espeak}); use a shorter path")
    ROOT.mkdir(parents=True, exist_ok=True)
    build = torch_build(args.torch)
    paths = {}
    if "stt" in engines:
        paths["STT_PY"] = environment("stt", "3.13", "stt.txt")
        paths["STT_MODEL"] = parakeet(pins)
    if "tts" in engines:
        paths["TTS_PY"] = environment("tts", "3.12", f"tts-{build}.txt", TORCH_INDEX[build])
        paths["TTS_MODEL"] = hf_files(pins, "kokoro", ROOT / "models/kokoro")
        paths["TTS_DEVICE"] = "cpu" if build == "cpu" else "gpu"
    if engines & {"detect", "turn"}:
        runtime = environment("runtime", "3.13", "runtime.txt")
    if "detect" in engines:
        paths["DETECT_PY"] = runtime
        paths["DETECT_MODEL"] = hf_files(pins, "injection", ROOT / "models/injection")
    if "turn" in engines:
        paths["TURN_PY"] = runtime
        train_build = "cuda" if args.train_device == "cuda" or (args.train_device == "auto" and build == "cuda") else "cpu"
        paths["TURN_MODEL"] = turn_model(pins, args, train_build)
    (ROOT / "engines.env").write_text("".join(f"{k}={v}\n" for k, v in paths.items()))
    say("ready:", ", ".join(sorted(engines)), f"(torch build {build}); paths in {ROOT}/engines.env")


if __name__ == "__main__":
    sys.exit(main())
