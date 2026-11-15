"""Decision questions about note-taker messages, with the answers from the specs (README.md).

  python3 -I evals/decide/eval_notes.py run CASES_GLOB --url URL --out RESULTS.jsonl [--workers W] [--limit N]
  python3 -I evals/decide/eval_notes.py report --dev DEV.jsonl --test TEST.jsonl [--temps-out T.json]

`run` asks each question of each case and writes the label log-probabilities.
`report` fits one temperature for each question on dev and gives accuracy,
ECE, and Brier score on test, raw and calibrated.
"""

import argparse
import concurrent.futures as cf
import glob
import json
import math
import re
import sys
import threading
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import decide as D  # noqa: E402

QUIET = {"question", "request", "plan", "hypothetical", "small-talk", "restate"}
KIND = {"change": "change", "other-update": "change", "fix": "correction", "end": "ended", "event": "happening"}
KIND_OPTIONS = {
    "new": "a new fact",
    "change": "a change of a fact that the notes have",
    "correction": "a correction: a fact in the notes was wrong",
    "ended": "a fact in the notes is no longer true",
    "happening": "a notable happening (an event)",
    "nothing": "nothing new: only questions, requests, plans, wishes, small talk, or facts that the notes have",
}


def state_of(c):
    notes = c["notes"].strip() or "(no notes yet)"
    return f"Notes of the assistant about {c['self']}:\n{notes}\n\nMessage from {c['self']} to the assistant:\n{c['text']}"


def walk(checks):
    for k in checks:
        yield k
        yield from walk(k.get("any_of") or [])


def kinds_of_notes(notes):
    """label -> kind, from lines like "- [ent_x] Waffles (animal)"."""
    return {m.group(1): m.group(2) for m in re.finditer(r"^- \[ent_\w+\] (.+?) \((\w+)\)", notes, re.M)}


def claims_of_notes(notes):
    """(subject label, relation) of each claim in the notes."""
    out, ent = set(), None
    for line in notes.splitlines():
        m = re.match(r"^- \[ent_\w+\] (.+?) \(\w+\)", line)
        if m:
            ent = m.group(1)
            continue
        m = re.match(r"^\s+- \[clm_\w+\] (.*)$", line)
        if m and ent and m.group(1).startswith(ent + " "):
            out.add((ent, m.group(1)[len(ent) + 1:].split(" ")[0]))
    return out


def changes_note(c):
    """A has or lacks check on a subject and relation that the notes have."""
    have = claims_of_notes(c["notes"])
    self_label = next((e for e, k in kinds_of_notes(c["notes"]).items() if e == c["self"]), c["self"])
    has_subs = {k["has"]["subject"] for k in walk(c["graph"]) if k.get("has")}
    for k in walk(c["graph"]):
        for key in ("has", "lacks"):
            h = k.get(key)
            # A lacks check on another subject is a guard ("not my job"), not a change.
            if not h or (key == "lacks" and h["subject"] not in has_subs):
                continue
            sub = self_label if h["subject"] == "me" else h["subject"]
            rels = h.get("relation") or []
            if any(e == sub and (not rels or r in rels) for e, r in have):
                return True
    return False


def questions(c):
    """The questions of a case and their correct labels."""
    s = c["self"]
    kinds = [p["kind"] for p in c["parts"]]
    facts = [k for k in kinds if k not in QUIET]
    qs = [
        ({"id": "record", "type": "noul",
          "question": "Does the message tell something new that the notes must keep: a new or changed lasting fact, "
                      "a correction, a fact that is no longer true, or a notable happening? Questions, requests, plans, "
                      "wishes, small talk, and facts that the notes already have do not count."},
         "A" if facts else "B"),
        ({"id": "asks", "type": "noul",
          "question": f"Does {s} ask the assistant a question to answer (not a task to do)?"},
         "A" if "question" in kinds else "B"),
        ({"id": "task", "type": "noul",
          "question": f"Does {s} ask the assistant to do a task other than keeping notes (a reminder, a shopping or "
                      "to-do list item, a booking, a renewal)? Asking the assistant to note or remember facts does not count."},
         "A" if "request" in kinds else "B"),
    ]
    g = c["graph"]
    n = sum(1 for k in g if "has" in k or "ended" in k or "any_of" in k)
    if facts or n == 0:
        qs.append(({"id": "count", "type": "score", "max": 4,
                    "question": "How many separate new facts, changes, corrections, ended facts, and happenings does "
                                "the message tell? Questions, requests, plans, wishes, and small talk count 0."},
                   str(min(n, 4))))
    ek = kinds_of_notes(c["notes"])
    subs = {k["has"]["subject"] for k in walk(g) if k.get("has")}
    if subs and facts and c["category"] not in ("reports-to-me", "other-near-self") and len(subs) == 1:
        x = next(iter(subs))
        k = ek.get(x)
        if x == "me":
            cls = "A"
        elif k == "animal" or (k is None and c["category"] == "pet-ref"):
            cls = "C"
        elif k == "person" or (k is None and re.match(r"^(Dr\. )?[A-Z][a-z]+( [A-Z][a-z]+)?$", x)):
            cls = "B"
        elif k is not None:
            cls = "D"
        else:
            cls = None
        if cls:
            qs.append(({"id": "about", "type": "choice",
                        "question": "Who or what is the new fact of the message about?",
                        "options": [f"{s}, the writer", "another person", "a pet", "a thing, a place, or a project"]}, cls))
    if len(facts) <= 1 and c["category"] != "mixed":
        k = "nothing" if not facts else KIND.get(facts[0]) or ("change" if changes_note(c) else "new")
        keys = list(KIND_OPTIONS)
        qs.append(({"id": "kind", "type": "choice", "question": "What is the news of the message?",
                    "options": [KIND_OPTIONS[x] for x in keys]}, "ABCDEF"[keys.index(k)]))
    return qs


def run(a):
    cases = [json.loads(l) for f in sorted(glob.glob(a.cases)) for l in open(f) if l.strip()]
    if a.limit:
        cases = cases[:a.limit]
    dec = D.Decider(a.url)
    local = threading.local()
    slots = iter(range(a.workers))
    lock = threading.Lock()

    def one(c):
        if not hasattr(local, "slot"):
            with lock:
                local.slot = next(slots)
        st = state_of(c)
        rows = []
        for q, truth in questions(c):
            if a.only and q["id"] not in a.only.split(","):
                continue
            lp, tm = dec.logprobs(st, q, local.slot)
            rows.append({"case": c["id"], "category": c["category"], "q": q["id"], "labels": D.labels_for(q),
                         "logprobs": lp, "truth": truth, "ms": tm.get("prompt_ms", 0) + tm.get("predicted_ms", 0),
                         "prompt_n": tm.get("prompt_n", 0), "cache_n": tm.get("cache_n", 0)})
        return rows

    with open(a.out, "w") as f, cf.ThreadPoolExecutor(a.workers) as ex:
        done = 0
        for rows in ex.map(one, cases):
            for r in rows:
                f.write(json.dumps(r) + "\n")
            done += 1
            if done % 100 == 0:
                print(f"{done}/{len(cases)}", file=sys.stderr, flush=True)


def probs(r, t):
    return D.softmax([x / t for x in r["logprobs"]])


def nll(rows, t):
    return -sum(math.log(max(probs(r, t)[r["labels"].index(r["truth"])], 1e-12)) for r in rows) / len(rows)


def fit(rows):
    grid = [math.exp(x / 20) for x in range(-20, 121)]  # 0.37 .. 403
    return min(grid, key=lambda t: nll(rows, t))


def metrics(rows, t):
    acc = ece = brier = 0.0
    bins = [[0, 0.0, 0.0] for _ in range(10)]
    for r in rows:
        p = probs(r, t)
        i = r["labels"].index(r["truth"])
        b = max(range(len(p)), key=p.__getitem__)
        ok = b == i
        acc += ok
        brier += sum((x - (j == i)) ** 2 for j, x in enumerate(p))
        k = min(int(p[b] * 10), 9)
        bins[k][0] += 1
        bins[k][1] += p[b]
        bins[k][2] += ok
    n = len(rows)
    ece = sum(abs(c - o) for m, c, o in bins if m) / n
    return {"n": n, "acc": acc / n, "ece": ece, "brier": brier / n}


def report(a):
    dev = [json.loads(l) for l in open(a.dev)]
    test = [json.loads(l) for l in open(a.test)]
    temps = {}
    print(f"{'question':8} {'n':>5} {'T':>7}  {'acc':>6}  {'ECE raw':>8} {'ECE cal':>8}  {'Brier raw':>9} {'Brier cal':>9}")
    for q in sorted({r["q"] for r in test}):
        d = [r for r in dev if r["q"] == q]
        t = fit(d) if d else 1.0
        temps[q] = t
        rows = [r for r in test if r["q"] == q]
        raw, cal = metrics(rows, 1.0), metrics(rows, t)
        print(f"{q:8} {raw['n']:5d} {t:7.2f}  {cal['acc']:6.1%}  {raw['ece']:8.3f} {cal['ece']:8.3f}  {raw['brier']:9.3f} {cal['brier']:9.3f}")
    ms = sorted(r["ms"] for r in test)
    hit = sum(r["cache_n"] > 0 for r in test) / len(test)
    print(f"median {ms[len(ms) // 2]:.0f} ms, p90 {ms[int(len(ms) * 0.9)]:.0f} ms for each question; prompt cache used in {hit:.0%}")
    if a.temps_out:
        Path(a.temps_out).write_text(json.dumps(temps, indent=1) + "\n")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run")
    r.add_argument("cases")
    r.add_argument("--url", required=True)
    r.add_argument("--out", required=True)
    r.add_argument("--workers", type=int, default=4)
    r.add_argument("--limit", type=int, default=0)
    r.add_argument("--only", default="", help="question ids, comma-separated")
    p = sub.add_parser("report")
    p.add_argument("--dev", required=True)
    p.add_argument("--test", required=True)
    p.add_argument("--temps-out", default="")
    a = ap.parse_args()
    run(a) if a.cmd == "run" else report(a)


if __name__ == "__main__":
    main()
