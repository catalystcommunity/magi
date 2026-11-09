"""Ask the questions of a world set (questions-*.json) as the person, through
the magi command, after the world is in place (README.md). While each
question runs, poll "magi activity" every second to check that the busy
signal shows.

  python3 -I evals/world/run.py SET.json --magi PATH --token-file FILE
          [--server URL] [--label L] [--only ID,ID]
"""

import argparse
import datetime
import json
import os
import re
import subprocess
import sys
import threading
import time
from pathlib import Path

META = re.compile(r"^  (-> |[a-z][a-z ]*: )")


def run(args, *a, timeout=420):
    env = dict(os.environ, MAGI_TOKEN_FILE=args.token_file)
    cmd = [args.magi] + (["--server", args.server] if args.server else []) + list(a)
    return subprocess.run(cmd, env=env, capture_output=True, text=True, timeout=timeout)


def say(args, text, persona):
    seen, stop = [], threading.Event()

    def poll():
        while not stop.is_set():
            p = run(args, "activity", "-persona", persona, timeout=30)
            line = p.stdout.strip()
            if line and line != "idle":
                seen.append(line.split(" is ", 1)[-1])
            stop.wait(1.0)

    t = threading.Thread(target=poll, daemon=True)
    t.start()
    start = time.monotonic()
    p = run(args, "say", "-persona", persona, text)
    ms = int((time.monotonic() - start) * 1000)
    stop.set()
    t.join(5)
    steps = []
    for s in seen:
        if not steps or steps[-1] != s:
            steps.append(s)
    if p.returncode != 0:
        return {"error": (p.stderr or p.stdout).strip()[-500:], "ms": ms, "busy": steps}
    lines = p.stdout.strip().splitlines()
    head = max((i for i, l in enumerate(lines) if re.match(r"^\d\d:\d\d:\d\d .*\[utt_[^]]+\].*:$", l)), default=-1)
    model = re.search(r"\(([^)]+)\):$", lines[head]) if head >= 0 else None
    body = [l for l in lines[head + 1:] if not META.match(l)]
    return {"reply": "\n".join(body).strip(), "model": model.group(1) if model else "", "ms": ms, "busy": steps}


def score(case, reply, abstain_words, refusal_words=()):
    text = reply.lower().replace("’", "'")
    missing = [w for w in case.get("all", []) if w not in text]
    missing += ["|".join(g) for g in case.get("any", []) if not any(w in text for w in g)]
    present = [w for w in case.get("none", []) if w in text]
    present += [w for w, ok in case.get("none_unless", {}).items() if w in text and not any(o in text for o in ok)]
    # Version 2: regular expressions on the whole reply (dot matches new lines).
    f = re.S
    missing += [r for r in case.get("re_all", []) if not re.search(r, text, f)]
    missing += ["|".join(g) for g in case.get("re_any", []) if not any(re.search(r, text, f) for r in g)]
    # A match of re_none is excused when its sentence also says that it is
    # old ("slipped from December 1", "Ito, before the move"): the words
    # can come before or after the match.
    for r in case.get("re_none", []):
        bad = [m for m in re.finditer(r, text, f) if not EXCUSE.search(sentence(text, m.start()))]
        if bad:
            present.append(r)
    if not case.get("abstain"):
        # An answerable question: "I don't know" as the answer (the first
        # sentence) is a failure, not a pass. "No record of her quitting"
        # later in a reply is not a refusal.
        first = sentence(text, 0)
        present += ["refusal: " + w for w in refusal_words if w in first]
    ok = not missing and not present
    if case.get("abstain"):
        ok = ok and any(w in text for w in abstain_words)
    return {"ok": ok, "missing": missing, "present": present}


EXCUSE = re.compile(r"\b(slipped|moved|was|were|originally|earlier|previous|previously|old|before|used to|former|formerly|no longer|anymore|until|left|sold|later|after|now|current|currently|not yet|wasn't|was not|joined)\b")


def sentence(text, i):
    """The sentence of text that has the position i."""
    a = max(text.rfind(c, 0, i) for c in ".!?\n") + 1
    ends = [j for j in (text.find(c, i) for c in ".!?\n") if j >= 0]
    return text[a:min(ends) if ends else len(text)]


def extras(args):
    """The events and tasks that are not in the world (magi-world -extras)."""
    p = subprocess.run(args.extras, shell=True, capture_output=True, text=True, timeout=300)
    if p.returncode != 0:
        raise RuntimeError("extras: " + (p.stderr or p.stdout)[-300:])
    return [json.loads(l) for l in p.stdout.splitlines() if l.startswith("{")]


def key(x):
    return x.get("event", {}).get("event_id") or x.get("task", {}).get("task_id")


def field(x, path):
    v = x
    for k in path.split("."):
        v = v.get(k) if isinstance(v, dict) else None
    return v if isinstance(v, str) else json.dumps(v)


def verify(expect, new):
    """Each expectation needs one new item whose fields match its regular expressions."""
    out = []
    for e in expect:
        hit = None
        for x in new:
            if x.get("kind") == e["kind"] and all(re.search(r, field(x, k), re.S | re.I) for k, r in e.items() if k != "kind"):
                hit = x
                break
        out.append({"expect": e, "ok": hit is not None, "found": hit,
                    "near": None if hit else [x for x in new if x.get("kind") == e["kind"]][:3]})
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("set")
    ap.add_argument("--magi", default="magi")
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--server")
    ap.add_argument("--out", default=str(Path(__file__).parent / "results"))
    ap.add_argument("--label", default="")
    ap.add_argument("--only", default="")
    ap.add_argument("--no-actions", action="store_true")
    ap.add_argument("--extras", default="", help="command that prints magi-world -extras; actions with expect are checked with it")
    args = ap.parse_args()
    s = json.loads(Path(args.set).read_text())
    only = set(filter(None, args.only.split(",")))
    log = []

    def ask(kind, cid, case, persona):
        r = say(args, case["say"], persona)
        r.update(kind=kind, id=cid, say=case["say"], persona=persona, about=case.get("kind", ""))
        if "reply" in r and kind != "action":
            r.update(score(case, r["reply"], s["abstain_words"], s.get("refusal_words", [])))
        log.append(r)
        mark = "" if "ok" not in r else ("PASS " if r["ok"] else "FAIL ")
        busy = ",".join(r["busy"])[:80]
        print(f"{mark}{kind:8} {cid:4} {r['ms']:6} ms [{busy}] {case['say'][:50]!r} -> {r.get('reply', r.get('error', ''))[:200]!r}", flush=True)

    for q in s["questions"]:
        if not only or q["id"] in only:
            ask("question", q["id"], q, q.get("persona", s["persona"]))
    if not args.no_actions:
        for a in s.get("actions", []):
            if only and a["id"] not in only:
                continue
            seen = {key(x) for x in extras(args)} if args.extras and a.get("expect") else set()
            ask("action", a["id"], a, a.get("persona", s["persona"]))
            if a.get("expect") and args.extras:
                time.sleep(3)
                new = [x for x in extras(args) if key(x) not in seen]
                v = verify(a["expect"], new)
                ok = all(x["ok"] for x in v)
                log.append({"kind": "check", "id": a["id"] + ".v", "about": "direct check", "ok": ok, "verify": v, "new": new})
                print(f"{'PASS ' if ok else 'FAIL '}check    {a['id']}.v direct: {len(new)} new item(s)", flush=True)
            if "check" in a:
                c = a["check"]
                ask("check", a["id"] + ".c", c, c.get("persona", s["persona"]))
    scored = [r for r in log if "ok" in r]
    summary = {"set": s["version"], "label": args.label, "when": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
               "models": sorted({r.get("model", "") for r in log if r.get("model")}), "errors": sum("error" in r for r in log)}
    for kind in ["question", "check"]:
        rs = [r for r in scored if r["kind"] == kind]
        summary[kind] = f"{sum(r['ok'] for r in rs)}/{len(rs)}"
    summary["by_kind"] = {}
    for r in scored:
        k = summary["by_kind"].setdefault(r["about"] or r["kind"], [0, 0])
        k[0] += r["ok"]
        k[1] += 1
    timed = [r for r in log if "reply" in r]
    summary["busy_seen"] = f"{sum(bool(r['busy']) for r in timed)}/{len(timed)}"
    summary["busy_over_10s"] = f"{sum(bool(r['busy']) for r in timed if r['ms'] > 10000)}/{sum(r['ms'] > 10000 for r in timed)}"
    ms = sorted(r["ms"] for r in timed)
    if ms:
        summary["ms"] = {"median": ms[len(ms) // 2], "p90": ms[min(len(ms) - 1, int(len(ms) * 0.9))], "max": ms[-1]}
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    stem = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + s["version"] + (f"-{args.label}" if args.label else "")
    (out / f"{stem}.json").write_text(json.dumps({"summary": summary, "log": log}, indent=1) + "\n")
    print(json.dumps(summary))


if __name__ == "__main__":
    sys.exit(main())
