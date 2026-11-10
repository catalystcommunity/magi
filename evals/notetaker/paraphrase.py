"""Rewrite note-taker cases in other words (README.md, "Version 3").

  python3 -I evals/notetaker/paraphrase.py IN.jsonl... --out OUT_DIR --url URL --model M
          --key-file F [--keep N] [--workers W] [--seed S] [--per-file N] [--prefix P]

A model writes each message of the cases again in other words and styles.
The notes, the date, and the graph checks stay the same. A rewrite is kept
only when it has each name, word, and number that the checks look for, so
the checks stay correct. The template cases of gen2.py have few phrasings;
the rewrites give the variety of real messages.

The key file is read and sent as a bearer token; it is not written out.
"""

import argparse
import concurrent.futures as cf
import glob
import json
import random
import re
import sys
import urllib.request
from pathlib import Path

STYLES = [
    "a short text message, lower case, little punctuation",
    "a voice transcript: no punctuation, filler words like um or so",
    "a chatty message with a short greeting or a word about the day first",
    "a plain, complete sentence",
    "a hurried note with a typo or two in common words (not in names or numbers)",
    "a message with an emoji",
    "a casual message that puts the parts in another order",
    "a message that starts with the main point and adds a short comment",
    "a formal message",
    "a message that uses other words for the same thing",
]

MEANING = {
    "question": "It stays a question; it tells no new fact.",
    "request": "It stays a request to do something; it tells no new fact.",
    "plan": "It stays a plan or an intent; it is not done yet.",
    "hypothetical": "It stays a maybe, a wish, or a guess; it is not a fact.",
    "small-talk": "It stays small talk with no lasting fact.",
    "restate": "It says that a known fact is still true; nothing changed.",
    "change": "It says that a fact changed.",
    "fix": "It says that a recorded fact was wrong and gives the correct one.",
    "end": "It says that something is no longer true.",
    "event": "It tells of a one-time happening.",
    "reports-to-me": "The named person now reports to the speaker: the speaker is their manager.",
    "third-person": "It is about another person, not about the speaker.",
    "other-fact": "It is about another person, not about the speaker.",
    "other-update": "It is about another person, not about the speaker.",
    "other-near-self": "Keep who each fact is about.",
    "birthday": "Keep the age and that the birthday is today.",
}

PROMPT = """Rewrite this chat message from a person to their personal assistant. Write {k} versions. Each version is {styles}.

Each version must say it with other words and another sentence shape, as a different person would say it; a change of only case, punctuation, or an added emoji is not enough.
Keep the same meaning: the same facts, the same people, and who does what. Do not add facts, names, places, dates, or numbers. Do not remove a fact. {meaning}
Keep these exactly as written (same spelling): {keep}
Keep words such as "my", "our", "the dog", "my sister", and "me": they tell who a thing is.

Message: {text}

Answer with JSON only: {{"rewrites": ["...", "..."]}}"""


def keep_words(case):
    """The strings that the checks look for and that the text has."""
    text = case["text"].lower()
    out = set()

    def add(s):
        if s and isinstance(s, str) and s.lower() in text and s.lower() not in ("me", "myself"):
            out.add(s)

    for c in case.get("graph") or []:
        for k in ("has", "lacks", "any_has"):
            f = c.get(k)
            if f:
                add(f.get("subject"))
                add(f.get("other"))
        add(c.get("event"))
        add(c.get("no_entity"))
    for n in re.findall(r"\d+", case["text"]):
        out.add(n)
    # Names: words with a capital letter that are not the first word.
    for m in re.finditer(r"(?<=[^.!?]\s)([A-Z][a-zA-Z'-]+(?:\s+[A-Z][a-zA-Z'-]+)*)", case["text"]):
        w = m.group(1)
        if w not in ("I", "I'm", "I've", "I'd", "I'll", "Mondays", "Monday", "Hey", "Long", "Coffee", "Morning", "Quick", "Ugh", "Just"):
            out.add(w)
    return sorted(out, key=len, reverse=True)


def valid(case, rw, keep):
    low = rw.lower()
    for k in keep:
        if not re.search(r"(?<![\w])" + re.escape(k.lower()) + r"(?![\w])", low):
            return False
    if "<<" in rw or len(rw) > 400 or len(rw) < 3:
        return False
    # No new numbers.
    if set(re.findall(r"\d+", rw)) - set(re.findall(r"\d+", case["text"])):
        return False
    return rw.strip().lower() != case["text"].strip().lower()


def ask(url, model, key, prompt):
    body = {"model": model, "messages": [{"role": "user", "content": prompt}], "max_tokens": 4000, "temperature": 0.9}
    req = urllib.request.Request(url.rstrip("/") + "/chat/completions", data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json", **({"Authorization": "Bearer " + key} if key else {}),
                                          "User-Agent": "magi-evals-paraphrase/1"})
    with urllib.request.urlopen(req, timeout=300) as r:
        msg = json.load(r)["choices"][0]["message"]
    s = msg.get("content") or ""
    s = re.sub(r"(?s)<think>.*?</think>", "", s)
    m = re.search(r"(?s)\{.*\}", s)
    if not m:
        return []
    try:
        v = json.loads(m.group(0)).get("rewrites") or []
    except json.JSONDecodeError:
        return []
    return [x.strip() for x in v if isinstance(x, str)]


def one(case, a, key, seed):
    r = random.Random(seed)
    keep = keep_words(case)
    k = a.keep + 1
    styles = r.sample(STYLES, k)
    prompt = PROMPT.format(k=k, styles="; ".join(f"version {i + 1}: {s}" for i, s in enumerate(styles)),
                           meaning=MEANING.get(case["category"], ""), keep=", ".join(f'"{x}"' for x in keep) or "(nothing)",
                           text=case["text"])
    for _ in range(2):
        try:
            rws = ask(a.url, a.model, key, prompt)
        except Exception as e:  # noqa: BLE001 - a failed call is retried once, then skipped
            print("error", case["id"], type(e).__name__, getattr(e, "code", ""), file=sys.stderr)
            rws = []
        good = []
        for rw in rws:
            if valid(case, rw, keep) and rw not in good:
                good.append(rw)
        if good:
            break
    out = []
    for i, rw in enumerate(good[: a.keep]):
        c = dict(case)
        c["id"] = f"{case['id']}-p{i}"
        c["text"] = rw
        c["source"] = case["text"]
        out.append(c)
    return case["id"], out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("inputs", nargs="+")
    ap.add_argument("--out", required=True)
    ap.add_argument("--url", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--key-file", default="", help="file with the API key (optional)")
    ap.add_argument("--keep", type=int, default=1, help="rewrites to keep for each case")
    ap.add_argument("--workers", type=int, default=6)
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--per-file", type=int, default=500)
    ap.add_argument("--prefix", default="cases")
    a = ap.parse_args()
    key = Path(a.key_file).read_text().strip() if a.key_file else ""
    cases = []
    for pat in a.inputs:
        for f in sorted(glob.glob(pat)):
            cases += [json.loads(l) for l in open(f) if l.strip()]
    done, results, failed = 0, {}, 0
    with cf.ThreadPoolExecutor(a.workers) as ex:
        futs = [ex.submit(one, c, a, key, a.seed * 100003 + i) for i, c in enumerate(cases)]
        for fu in cf.as_completed(futs):
            cid, out = fu.result()
            results[cid] = out
            failed += not out
            done += 1
            if done % 50 == 0:
                print(f"{done}/{len(cases)} failed {failed}", file=sys.stderr, flush=True)
    rows = [c for case in cases for c in results.get(case["id"], [])]
    od = Path(a.out)
    od.mkdir(parents=True, exist_ok=True)
    for i in range(0, len(rows), a.per_file):
        with open(od / f"{a.prefix}-{i // a.per_file:03d}.jsonl", "w") as f:
            for c in rows[i: i + a.per_file]:
                f.write(json.dumps(c) + "\n")
    print(f"{len(rows)} rewrites of {len(cases)} cases ({failed} cases with none)")


if __name__ == "__main__":
    main()
