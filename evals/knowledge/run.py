"""Run a knowledge evaluation set against a Magi server, through the magi
command (as a person would). See README.md.

  python3 evals/knowledge/run.py SET.json --magi PATH --token-file FILE
          [--server URL] [--out DIR]

Use a new subject for each run: the set says its facts into an empty
knowledge store, and the changes modify them.
"""

import argparse
import datetime
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path


def magi(args, *a):
    env = dict(os.environ, MAGI_TOKEN_FILE=args.token_file)
    cmd = [args.magi] + (["--server", args.server] if args.server else []) + list(a)
    return subprocess.run(cmd, env=env, capture_output=True, text=True, timeout=300)


def dump(args):
    """All that the store gives the person, as text (magi know)."""
    p = magi(args, "know")
    return (p.stdout if p.returncode == 0 else "error: " + p.stderr).strip()


# Lines "  -> kind ref title" (attachments) and "  about: ..." (links) are
# not the reply. Their titles can have the words that a check looks for.
META = re.compile(r"^  (-> |[a-z][a-z ]*: )")


def say(args, text, persona):
    start = time.monotonic()
    p = magi(args, "say", "-persona", persona, text)
    ms = int((time.monotonic() - start) * 1000)
    if p.returncode != 0:
        return {"error": (p.stderr or p.stdout).strip()[-500:], "ms": ms}
    # The reply is the text after the last header line ("HH:MM:SS name [utt_x] (model):").
    lines = p.stdout.strip().splitlines()
    head = max((i for i, l in enumerate(lines) if re.match(r"^\d\d:\d\d:\d\d .*\[utt_[^]]+\].*:$", l)), default=-1)
    model = re.search(r"\(([^)]+)\):$", lines[head]) if head >= 0 else None
    body = [l for l in lines[head + 1:] if not META.match(l)]
    return {"reply": "\n".join(body).strip(), "model": model.group(1) if model else "", "ms": ms}


def score(case, reply, abstain_words):
    text = reply.lower().replace("’", "'")
    missing = [w for w in case.get("all", []) if w not in text]
    present = [w for w in case.get("none", []) if w in text]
    ok = not missing and not present
    if case.get("abstain"):
        ok = ok and any(w in text for w in abstain_words)
    return {"ok": ok, "missing": missing, "present": present}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("set")
    ap.add_argument("--magi", default="magi")
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--server")
    ap.add_argument("--out", default=str(Path(__file__).parent / "results"))
    ap.add_argument("--label", default="")
    args = ap.parse_args()
    s = json.loads(Path(args.set).read_text())
    log = []

    def ask(kind, cid, case):
        r = say(args, case["say"], s["tell_persona"] if kind in ("seed", "change") else s["ask_persona"])
        r.update(kind=kind, id=cid, say=case["say"])
        if "reply" in r and kind != "seed" and kind != "change":
            r.update(score(case, r["reply"], s["abstain_words"]))
        log.append(r)
        mark = "" if "ok" not in r else ("PASS " if r["ok"] else "FAIL ")
        print(f"{mark}{kind:8} {cid:4} {r['ms']:6} ms  {case['say'][:60]!r} -> {r.get('reply', r.get('error', ''))[:160]!r}", flush=True)

    for i, text in enumerate(s["seed"]):
        ask("seed", f"s{i + 1:02}", {"say": text})
    def store(cid, case):
        text = dump(args)
        r = {"kind": "store", "id": cid, "say": "(store)", "reply": text, "ms": 0}
        r.update(score(case, text, s["abstain_words"]))
        log.append(r)
        print(f"{'PASS' if r['ok'] else 'FAIL'} store    {cid:4} missing {r['missing']} present {r['present']}", flush=True)

    store("st1", s["store_after_seed"])
    for q in s["questions"]:
        ask("question", q["id"], q)
    for c in s["changes"]:
        ask("change", c["id"], c)
        for j, q in enumerate(c["check"]):
            ask("check", f"{c['id']}.{j + 1}", q)

    store("st2", s["store_after_changes"])
    scored = [r for r in log if "ok" in r]
    summary = {"set": s["version"], "label": args.label, "when": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
               "models": sorted({r.get("model", "") for r in log if r.get("model")}), "errors": sum("error" in r for r in log)}
    summary["store"] = {r["id"]: {"missing": r["missing"], "present": r["present"]} for r in scored if r["kind"] == "store"}
    for kind in ["question", "check"]:
        rs = [r for r in scored if r["kind"] == kind]
        summary[kind] = f"{sum(r['ok'] for r in rs)}/{len(rs)}"
    summary["abstain"] = f"{sum(r['ok'] for r in scored if r['id'].startswith('a'))}/{sum(r['id'].startswith('a') for r in scored)}"
    for c in s["changes"]:
        rs = [r for r in scored if r["id"].startswith(c["id"] + ".")]
        summary.setdefault("by_change_kind", {}).setdefault(c["kind"], [0, 0])
        summary["by_change_kind"][c["kind"]][0] += sum(r["ok"] for r in rs)
        summary["by_change_kind"][c["kind"]][1] += len(rs)
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    stem = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + s["version"] + (f"-{args.label}" if args.label else "")
    (out / f"{stem}.json").write_text(json.dumps({"summary": summary, "log": log}, indent=1) + "\n")
    print(json.dumps(summary))


if __name__ == "__main__":
    sys.exit(main())
