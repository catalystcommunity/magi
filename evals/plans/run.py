#!/usr/bin/env python3
"""Run a plans evaluation set (README.md) against a Magi installation."""
import argparse
import datetime
import json
import subprocess
import sys
import time
import zoneinfo
from pathlib import Path


def magi(args, *a):
    cmd = [args.magi] + (["--server", args.server] if args.server else []) + list(a)
    return subprocess.run(cmd, capture_output=True, text=True, env={"MAGI_TOKEN_FILE": args.token_file, "PATH": "/usr/bin:/bin"}, timeout=300)


def say(args, text, persona):
    t = time.time()
    p = magi(args, "say", "-persona", persona, text)
    ms = int((time.time() - t) * 1000)
    if p.returncode != 0:
        return {"error": p.stderr.strip()[-500:], "ms": ms}
    lines = p.stdout.strip().split("\n")
    # "say" prints the message, a blank line, the header, and the reply.
    for i, l in enumerate(lines):
        if l.startswith(tuple("0123456789")) and "] (" in l:
            return {"reply": "\n".join(lines[i + 1:]).strip(), "model": l.split("] (", 1)[1].rstrip("):"), "ms": ms}
    return {"reply": p.stdout.strip(), "ms": ms}


def todos(args):
    p = magi(args, "todos", "-all", "-json")
    if p.returncode != 0:
        raise RuntimeError(p.stderr)
    return json.loads(p.stdout or "[]") or []


DAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]


def want_time(spec, start, said, tz):
    """The expected time: (datetime, date_only) in zone tz."""
    if spec.startswith("now+"):
        return said + datetime.timedelta(minutes=int(spec[4:].rstrip("m"))), False
    day, _, hm = spec.partition(" ")
    today = start.date()
    if day.startswith("+"):
        d = today + datetime.timedelta(days=int(day[1:]))
    else:
        n = (DAYS.index(day) - today.weekday()) % 7 or 7
        d = today + datetime.timedelta(days=n)
    h, m = (int(x) for x in hm.split(":")) if hm else (0, 0)
    return datetime.datetime(d.year, d.month, d.day, h, m, tzinfo=tz), not hm


def matches(t, w, start, said, tz, ids, got_ids):
    title = t["title"].lower()
    if any(word not in title for word in w.get("title", [])):
        return "title"
    for k in ("kind", "state"):
        if k in w and t.get(k) != w[k]:
            return f"{k} {t.get(k)}"
    if "plan" in w and w["plan"] not in (t.get("plan") or "").lower():
        return "plan"
    for k, field in (("due", "due_ms"), ("remind", "remind_ms")):
        if k in w:
            if not t.get(field):
                return f"no {k}"
            got = datetime.datetime.fromtimestamp(t[field] / 1000, tz)
            exp, date_only = want_time(w[k], start, said, tz)
            if date_only and got.date() != exp.date() or not date_only and abs((got - exp).total_seconds()) > 300:
                return f"{k} {got:%a %d %b %H:%M}, want {exp:%a %d %b %H:%M}"
    if "after" in w and t.get("after") != ids.get(w["after"]):
        return "after"
    if w.get("new") and t["id"] == got_ids.get(w["key"]):
        return "not a new record"
    return ""


def check(args, step, start, said, tz, ids):
    """Waits up to 30 s for the to-dos of a step; gives the failures."""
    fails = []
    for attempt in range(16):
        ts = todos(args)
        fails = []
        found = {}
        for w in step.get("todos", []):
            key = w["key"]
            spec = dict(w)
            # A later step names a to-do by key; the title words stay.
            if "title" not in spec and key in args.titles:
                spec["title"] = args.titles[key]
            cands = [t for t in ts if not w.get("new") and t["id"] == ids.get(key)] or ts
            # "after" can name a to-do of the same step, found above.
            known = {**ids, **found}
            why = [(matches(t, spec, start, said, tz, known, ids), t) for t in cands]
            ok = [t for r, t in why if r == ""]
            if ok:
                found[key] = ok[0]["id"]
            else:
                near = [r for r, t in why if r != "title"]
                fails.append(f"{key}: {near[0] if near else 'missing'}")
        for words in step.get("absent", []):
            if any(all(x in t["title"].lower() for x in words) for t in ts):
                fails.append(f"present: {' '.join(words)}")
        for words in step.get("once", []):
            n = sum(all(x in t["title"].lower() for x in words) and t["state"] in ("open", "waiting", "late") for t in ts)
            if n != 1:
                fails.append(f"{' '.join(words)}: {n} open")
        if not fails:
            for key, tid in found.items():
                ids[key] = tid
            for w in step.get("todos", []):
                if "title" in w:
                    args.titles[w["key"]] = w["title"]
            return []
        time.sleep(2)
    return fails


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("set")
    ap.add_argument("--magi", default="magi")
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--server")
    ap.add_argument("--tz", default="UTC", help="the time zone of the household (MAGI_TIME_ZONE)")
    ap.add_argument("--out", default=str(Path(__file__).parent / "results"))
    ap.add_argument("--label", default="")
    args = ap.parse_args()
    args.titles = {}
    s = json.loads(Path(args.set).read_text())
    tz = zoneinfo.ZoneInfo(args.tz)
    start = datetime.datetime.now(tz)
    ids, log = {}, []
    for st in s["steps"]:
        said = datetime.datetime.now(tz)
        r = say(args, st["say"], s["persona"])
        r.update(id=st["id"], say=st["say"])
        fails = []
        if "error" in r:
            fails.append("error: " + r["error"])
        else:
            text = r["reply"].lower()
            fails += [f"reply lacks {w!r}" for w in st.get("reply_all", []) if w not in text]
            fails += [f"reply has {w!r}" for w in st.get("reply_none", []) if w in text]
            fails += check(args, st, start, said, tz, ids)
        r.update(ok=not fails, fails=fails)
        log.append(r)
        print(f"{'PASS' if r['ok'] else 'FAIL'} {st['id']} {r['ms']:6} ms  {st['say'][:55]!r} -> {r.get('reply', r.get('error', ''))[:140]!r} {fails or ''}", flush=True)
    ms = sorted(r["ms"] for r in log if "reply" in r)
    summary = {"set": s["version"], "label": args.label, "when": start.isoformat(timespec="seconds"),
               "models": sorted({r.get("model", "") for r in log if r.get("model")}),
               "steps": f"{sum(r['ok'] for r in log)}/{len(log)}",
               "ms": {"median": ms[len(ms) // 2], "p90": ms[min(len(ms) - 1, int(len(ms) * 0.9))], "max": ms[-1]} if ms else {}}
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    stem = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + s["version"] + (f"-{args.label}" if args.label else "")
    (out / f"{stem}.json").write_text(json.dumps({"summary": summary, "log": log}, indent=1) + "\n")
    print(json.dumps(summary))


if __name__ == "__main__":
    sys.exit(main())
