#!/usr/bin/env python3
"""Training records of the destination question for a decision LoRA.

  python3 -I evals/egress/export_train.py --out DIR [--train FILE] [--seed S] [--look-alikes N]

Each host of the train file becomes one record: the prompt of the decision
model (evals/decide/decide.py) with the state and the question of
magi/compose/egressauto.go, and the label ("A" yes, "B" no) as the answer.
The records have the plain prompt of llama-server for Gemma 4, as
evals/decide/export_train.py --format gemma4 writes it, so train_lora.py
of evals/notetaker trains on them together with the note questions.

The export also makes names that look like the known hosts of the train
file (one different letter, a different domain, the name inside a longer
name). Their label is "no". It makes them only from train hosts: the test
corpus (corpus1.json) has look-alikes that a different writer made.

Each record gets a task title from a list. The titles of the eval are not
in the list.
"""
import argparse
import json
import os
import random
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent / "decide"))
import decide as D  # noqa: E402

sys.path.insert(0, str(HERE))
from decide_destinations import QUESTION  # noqa: E402

GEMMA4 = "<bos><|turn>system\n{system}<turn|>\n<|turn>user\n{user}<turn|>\n<|turn>model\n<|channel>thought\n<channel|>"
Q = {"id": "egress-known-source", "type": "noul", "question": QUESTION}

TITLES = [
    "Add retries to the upload client", "Fix the date parser for week numbers", "Build the documentation site", "Update the Rust toolchain",
    "Add a health endpoint", "Write a migration for the orders table", "Speed up the image resize job", "Add OAuth login",
    "Fix the flaky queue test", "Port the CLI to Windows", "Add a Dockerfile for the worker", "Generate the API client from the schema",
    "Add Prometheus metrics", "Fix the memory leak in the cache", "Upgrade the web framework", "Add a spell checker to the editor",
    "Write unit tests for the tokenizer", "Add TLS to the gRPC server", "Fix time zone handling in reports", "Add a PDF export",
    "Train the intent classifier", "Compile the firmware for the new board", "Add full-text search", "Fix the Android build",
    "Add a rate limiter", "Vendor the dependencies", "Set up continuous integration", "Add localization for German",
    "Fix the CSV importer for quoted fields", "Add a GraphQL endpoint", "Package the app for Debian", "Profile the startup time",
    "Add WebSocket reconnect", "Fix the Unicode normalization bug", "Add a backup command", "Update the Kubernetes manifests",
    "Write the changelog generator", "Add image thumbnails", "Fix the race in the scheduler", "Add an audit log",
]
TLDS = ["ru", "cn", "xyz", "top", "cc", "info", "download", "click", "site", "online", "net", "co", "io", "dev", "app", "org", "com", "biz", "tk", "pw"]
WORDS = ["cdn", "mirror", "dl", "download", "pkg", "files", "static", "assets", "proxy", "cache", "get", "secure", "update", "repo"]
OWNERS = ["fastmirror", "pkgcache", "opendl", "netstash", "bluefile", "quickrepo", "dlzone", "mirrorhub", "softbay", "hostwave", "cloudnest", "datapier"]
KEYS = "abcdefghijklmnopqrstuvwxyz"
SWAPS = {"m": "rn", "l": "1", "o": "0", "i": "l", "w": "vv", "g": "q", "e": "3", "s": "5"}


def look_alikes(host, rnd, n):
    """n names that look like host and are not host."""
    labels = host.split(".")
    i = max(range(len(labels) - 1), key=lambda j: len(labels[j])) if len(labels) > 1 else 0
    name = labels[i]
    out = set()
    for _ in range(n * 8):
        if len(out) >= n:
            break
        kind = rnd.randrange(8)
        l = list(labels)
        if kind == 0 and len(name) > 3:  # drop a letter
            j = rnd.randrange(len(name)); l[i] = name[:j] + name[j + 1:]
        elif kind == 1 and len(name) > 2:  # double a letter
            j = rnd.randrange(len(name)); l[i] = name[:j] + name[j] + name[j:]
        elif kind == 2 and len(name) > 3:  # change places of two letters
            j = rnd.randrange(len(name) - 1); l[i] = name[:j] + name[j + 1] + name[j] + name[j + 2:]
        elif kind == 3:  # a different letter
            j = rnd.randrange(len(name)); c = rnd.choice(KEYS); l[i] = name[:j] + c + name[j + 1:]
        elif kind == 4:  # a letter that looks the same
            js = [j for j, c in enumerate(name) if c in SWAPS]
            if not js:
                continue
            j = rnd.choice(js); l[i] = name[:j] + SWAPS[name[j]] + name[j + 1:]
        elif kind == 5:  # a different top-level domain
            l[-1] = rnd.choice([t for t in TLDS if t != l[-1]])
        elif kind == 6:  # a word with the name, in a different domain
            w = rnd.choice(WORDS)
            l = [rnd.choice([f"{w}-{name}", f"{name}-{w}", f"{name}{w}"]), rnd.choice(TLDS)]
        else:  # the whole host inside a different domain
            l = labels + [rnd.choice(OWNERS), rnd.choice(TLDS)]
        cand = ".".join(l)
        if cand != host and all(l):
            out.add(cand)
    return sorted(out)


def record(host, title, yes):
    state = f"A task environment wants to connect to a host that the network policy does not list.\nHost: {host}\nPort: 443\nTask: {title}"
    return {"prompt": GEMMA4.format(system=D.SYSTEM, user=D.user_prompt(state, Q)), "completion": ("A" if yes else "B") + "<turn|>"}


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--train", default=str(HERE / "train2.json"))
    p.add_argument("--out", required=True)
    p.add_argument("--seed", type=int, default=1)
    p.add_argument("--look-alikes", type=int, default=2, help="names that look like each known host")
    p.add_argument("--per-file", type=int, default=5000)
    a = p.parse_args()
    rnd = random.Random(a.seed)
    d = json.load(open(a.train))
    known = {x["host"] for x in d["allow"]}
    recs = []
    for x in d["allow"]:
        # Two titles for each known host: the answer does not depend on the task.
        for title in rnd.sample(TITLES, 2):
            recs.append(record(x["host"], title, True))
        for fake in look_alikes(x["host"], rnd, a.look_alikes):
            if fake not in known:
                recs.append(record(fake, rnd.choice(TITLES), False))
    for x in d["ask"]:
        recs.append(record(x["host"], rnd.choice(TITLES), False))
    rnd.shuffle(recs)
    out = Path(a.out)
    out.mkdir(parents=True, exist_ok=True)
    for i in range(0, len(recs), a.per_file):
        with open(out / f"train-egress-{i // a.per_file:03d}.jsonl", "w") as f:
            for r in recs[i:i + a.per_file]:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
    yes = sum(r["completion"].startswith("A") for r in recs)
    print(f"{len(recs)} records: {yes} yes, {len(recs) - yes} no")


if __name__ == "__main__":
    main()
