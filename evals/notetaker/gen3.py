"""Make the specs of note-taker cases, version 5 (README.md, "Version 5").

  python3 -I evals/notetaker/gen3.py OUT_DIR --part train|dev|test [--n PER_CATEGORY]
          [--mixed N] [--seed S] [--per-file N] [--only CATS]

A spec is a case of gen2.py without the text: the notes, the graph checks,
and the parts that the message must say. write3.py gives the parts to a
model, which writes the message in a style (a short text, a long ramble
with side remarks, a voice transcript, ...). The checks come from the
spec, so they stay correct for each wording.

Changes from gen2.py:

- "mixed": 2 to 4 facts of other categories in one world and one message,
  and 0 to 2 parts that must not change the notes (a question, a plan, a
  wish). All the checks of the parts apply.
- Most cases get 1 or 2 more parts that must not change the notes, so the
  facts are not the only thing in the message.
- A happening that is also a fact ("Ezra lost a first tooth today") is right
  as an event or as a fact: the checks accept either.
- A hypothetical can tell of a past happening ("a recruiter called"); it
  must not change a fact, but an event is not wrong.
"""

import argparse
import json
import random
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gen2 as G  # noqa: E402

QUIET_CATS = ["small-talk", "question", "request", "plan", "restate", "hypothetical"]
FACT_CATS = [c for c in G.MAKERS if c not in QUIET_CATS]

# What a part means, for the writer and the checker.
MEANING = {
    "question": "a question to the assistant; it tells no new fact",
    "request": "a request to the assistant to do something; it tells no new fact",
    "plan": "only a maybe or a plan; nothing is done or decided",
    "hypothetical": "only a wish, a guess, or a thing that did not happen; it changes no fact",
    "small-talk": "passing small talk with no lasting fact",
    "restate": "a known fact that is still true; nothing changed",
    "change": "a fact that changed",
    "fix": "a correction: a recorded fact was wrong, and this is the right one",
    "end": "a thing that is no longer true",
    "event": "a happening",
    "reports-to-me": "the named person now reports to the writer (the writer is their manager)",
    "third-person": "a fact about another person, not about the writer",
    "other-fact": "a fact about another person, not about the writer",
    "other-update": "a fact about another person, not about the writer",
    "other-near-self": "facts about another person; keep who each fact is about",
    "birthday": "the writer's birthday is today, with this age",
    "alias": "a fact about the person named by this short name",
    "kin": "a fact about this relative or person of the writer",
    "same-name": "a fact about this exact person (other people have the same first name)",
    "pet-ref": "a fact about the pet",
    "multi-fact": "several facts; keep each of them",
    "new-fact": "a new fact",
}

STYLES = [
    "a short text message, lower case, little punctuation",
    "a voice transcript: no punctuation, filler words (um, so, like), a restart or two",
    "a long ramble that tells the day first and gets to the point late",
    "a run-on message with many 'and' and 'oh and', parts in a random order",
    "a tired parent typing fast with typos in common words (not in names or numbers)",
    "a formal note, like an email to an assistant",
    "a very terse note, fragments only",
    "a chatty message to a friend, with an emoji or two",
    "a list with dashes, one line for each thing, with a short lead-in",
    "a message with side comments in parentheses",
    "a message from a person who writes English as a second language, simple grammar",
    "a message that starts with a complaint about the day, then the news",
    "a story told in order: what happened, then what it means now",
    "a message that asks the assistant to note things, then tells them",
    "a message with sarcasm and jokes, but the facts are clear",
    "a stream of thought that jumps from one thing to the next",
    "a calm, clear message with full sentences",
    "a message written in a hurry between meetings, abbreviations like w/, bc, tmrw",
    "a message that buries the main news in the middle of other talk",
    "a message that tells the news indirectly, as a person would (\"guess who has a new job\")",
    "a message in two paragraphs: one of other things, one of the news",
    "a teen-style text with slang, but the facts are clear",
    "a message that quotes what another person said, then comments",
    "a long message with details about feelings and the day around the facts",
    "a polite message that thanks the assistant first",
    "a voice note while driving: short phrases, 'wait', 'anyway'",
    "a message that mentions the facts as an aside to another topic",
    "an excited message with exclamation marks and caps on one or two words",
    "a dry, matter-of-fact message",
    "a message that is mostly about something else, with the facts at the end",
]

def quiet_part(r, avoid, no_event=False):
    """A part that must not change the notes, from the phrase lists of gen2.py.
    no_event: not small talk, which can tell a happening (a case with a "quiet" check)."""
    cat = r.choice(["question", "request", "plan"] if no_event else ["small-talk", "question", "request", "plan"])
    lst = {"small-talk": G.SMALL, "question": G.QUESTIONS, "request": G.REQUESTS, "plan": G.PLANS}[cat]
    for _ in range(20):
        o, kid = G.P(r, G.FIRST), G.P(r, G.KIDS)
        if o not in avoid and kid not in avoid:
            break
    t = G.P(r, lst).format(o=o, city=G.P(r, G.CITIES), team=G.P(r, G.TEAMS), kid=kid)
    return {"say": t, "kind": cat, "meaning": MEANING[cat]}


def names_of(text):
    return {m for m in re.findall(r"\b[A-Z][a-z]+(?:'s)?\b", text)} - {"I", "My", "Our", "The", "We", "Dr", "New", "Just"}


def fix_checks(cat, checks):
    out = []
    for c in checks:
        if "event" in c:
            # A happening that is also a fact: an event or a fact of anyone.
            v = c["event"]
            out.append({"any_of": [{"event": v}, {"any_has": {"subject": "", "other": v}}]})
        elif c.get("quiet") and cat == "hypothetical":
            out.append({"no_change": True})
        else:
            out.append(c)
    return out


def birthday(checks, at):
    out, n = [], None
    for c in checks:
        if "n" in c:
            n = c["n"]
            continue
        out.append(c)
    for c in out:
        h = c.get("has")
        if h and h.get("re") == "__BIRTHDAY__":
            m, d = at.strftime("%B"), at.day
            h["re"] = [rf"(?i)\b{m[:3]}[a-z]*\.?\s+{d}(st|nd|rd|th)?\b|\b{d}(st|nd|rd|th)?\s+(of\s+)?{m[:3]}|-{at.month:02d}-{d:02d}\b",
                       rf"^(?:.*\b{at.year - n}\b.*|(?:[^0-9]|\b\d{{1,2}}\b)*)$"]
    return out


def single(r, cat):
    w, t, checks = G.MAKERS[cat](r)
    parts = [{"say": t, "kind": cat, "meaning": MEANING.get(cat, "")}]
    k = r.choices([0, 1, 2], [0.35, 0.45, 0.2])[0]
    for _ in range(k):
        q = quiet_part(r, names_of(" ".join(p["say"] for p in parts)), no_event=any(c.get("quiet") for c in checks))
        if q["say"] not in [p["say"] for p in parts]:
            parts.append(q)
    r.shuffle(parts)
    return w, parts, fix_checks(cat, checks)


def rel_overlap(a, b):
    if not a or not b:
        return True
    return any(x in y or y in x for x in a for y in b)


def conflicts(new_checks, old_checks):
    """A has or lacks of a part on the same subject and relation as another part."""
    def facts(cs):
        for c in cs:
            for k in ("has", "lacks"):
                if c.get(k):
                    yield c[k]
    for a in facts(new_checks):
        for b in facts(old_checks):
            if a["subject"].lower() == b["subject"].lower() and rel_overlap(a.get("relation"), b.get("relation")):
                return True
    return False


def mixed(r):
    """2 to 4 facts of other categories and 0 to 2 quiet parts in one world."""
    shared = []
    orig = G.W

    def world(rr):
        if not shared:
            shared.append(orig(rr))
        return shared[0]

    G.W = world
    try:
        parts, checks, names = [], [], set()
        want = r.choice([2, 2, 3, 3, 4])
        pool = [c for c in FACT_CATS if c not in ("birthday", "multi-fact")]
        for _ in range(40):
            if len([p for p in parts if p["kind"] not in QUIET_CATS]) >= want:
                break
            cat = r.choice(pool)
            w0 = shared[0] if shared else None
            snap = (list(w0.facts), dict(w0.ents)) if w0 else None
            w, t, cks = G.MAKERS[cat](r)
            cks = fix_checks(cat, cks)
            bad = bool(names_of(t) & names) or conflicts(cks, checks)
            if snap and not bad:
                # A name of the part is the name of a person of the world.
                known = {x for label in snap[1] for x in re.findall(r"[A-Z][a-z]+", label)}
                bad = bool({n.removesuffix("'s") for n in names_of(t)} & known)
            if snap and not bad:
                old = {f[0] for f in snap[0]}
                now = {f[0] for f in w.facts}
                bad = not old <= now
                # one current fact for a subject and a relation
                seen, full = set(), set()
                for f in w.facts:
                    key = (f[1], f[2])
                    if (key in seen and f[2] not in ("child", "dog", "cat")) or (f[1], f[2], f[3]) in full:
                        bad = True
                    seen.add(key)
                    full.add((f[1], f[2], f[3]))
            if bad:
                if snap:
                    w.facts[:], w.ents = snap[0], snap[1]
                continue
            parts.append({"say": t, "kind": cat, "meaning": MEANING.get(cat, "")})
            checks += cks
            names |= names_of(t)
        w = shared[0]
        for _ in range(r.choice([0, 1, 1, 2])):
            q = quiet_part(r, names)
            if q["say"] in [p["say"] for p in parts]:
                continue
            parts.append(q)
            names |= names_of(q["say"])
    finally:
        G.W = orig
    r.shuffle(parts)
    return w, parts, checks


def make(r, cat, i):
    w, parts, checks = mixed(r) if cat == "mixed" else single(r, cat)
    at = G.when(r)
    checks = birthday(checks, at)
    noise = r.choices(["none", "light", "heavy"], [0.25, 0.4, 0.35])[0]
    return {"id": f"{cat}-{G.PART}-v5-{i:05d}", "category": cat, "when": at.isoformat(), "self": w.self, "notes": w.notes(),
            "text": "", "expect": [], "forbid": [], "allow_extra": True, "graph": checks,
            "parts": parts, "style": r.choice(G.part(STYLES)), "noise": noise}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("--part", required=True, choices=["train", "dev", "test"])
    ap.add_argument("--n", type=int, default=45, help="cases for each category")
    ap.add_argument("--mixed", type=int, default=-1, help="cases of mixed (default: 2 n)")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--per-file", type=int, default=500)
    ap.add_argument("--only", default="")
    a = ap.parse_args()
    G.PART = a.part
    r = random.Random(a.seed)
    cats = [c for c in list(G.MAKERS) + ["mixed"] if not a.only or c in a.only.split(",")]
    cases = []
    for cat in cats:
        n = (a.mixed if a.mixed >= 0 else 2 * a.n) if cat == "mixed" else a.n
        cases += [make(r, cat, i) for i in range(n)]
    r.shuffle(cases)
    out = Path(a.out)
    out.mkdir(parents=True, exist_ok=True)
    for k in range(0, len(cases), a.per_file):
        with open(out / f"spec-{k // a.per_file:03d}.jsonl", "w") as f:
            for c in cases[k:k + a.per_file]:
                f.write(json.dumps(c, ensure_ascii=False) + "\n")
    print(f"{len(cases)} specs, {len(cats)} categories")


if __name__ == "__main__":
    main()
