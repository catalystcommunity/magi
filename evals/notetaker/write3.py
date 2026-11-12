"""Write the messages of the specs of gen3.py (README.md, "Version 5").

  python3 -I evals/notetaker/write3.py SPEC.jsonl... --out OUT_DIR --url URL --model M [--key-file F]
          --check-url URL --check-model M [--check-key-file F] [--workers W] [--tries N] [--prefix P]

A writer model writes the message of each spec in its style. A rewrite is
kept only when:

1. It has each name, word, and number that the checks look for, and no
   new number (as paraphrase.py).
2. A checker model reads it and says that it tells each part with the same
   meaning (a plan stays a plan, a fact is about the same person), and no
   other lasting fact.

The key files are read and sent as bearer tokens; they are not written out.
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

sys.path.insert(0, str(Path(__file__).resolve().parent))
import paraphrase  # noqa: E402

WRITE = """Write one chat message that {self} sends to their personal assistant (an AI that keeps notes about {self}'s life).

The message must tell each of these, with the same meaning:
{parts}

Style: {style}.
{noise}

Rules:
- Use your own words, as a real person would write; do not copy the lines above. Change the order, join or split sentences, be indirect where a person would be. A careful reader must still get the same meaning.
- Each fact stays with the same person. A line with no name of a person or pet is about {self}, the writer: keep it about {self} ("I", "my", "we"). Words like "my", "our", "the dog", "my sister", and the names tell who a thing is about.
- A question stays a question, a request stays a request, a plan or a wish stays a plan or a wish, a change still says that it changed.
- Keep these exactly as written (same spelling): {keep}
- Do not add facts, people, pets, places, organizations, dates, or numbers. Side remarks are passing things only (moods, small stuff of the day), not lasting facts or notable happenings.
- Do not sign the message.

Answer with JSON only: {{"message": "..."}}"""

CHECK = """{self} sent this message to their personal assistant:

<<<
{text}
>>>

These are the things the message must tell:
{parts}

For each thing: does the message tell it, with the same meaning? A question must still be a question (about the same thing), a plan or a wish must still be only a plan or a wish, a change must still be a change, and each fact must be about the same person or pet. A thing with no name of a person or pet is about {self}, the writer: the message must tell it about {self} ("I", "my", "we"), not about another person. The words can be different.

Then: does the message tell any OTHER lasting fact that a note taker must keep: a new person or a relationship, a job, a home, a pet, a date, a plan, a health problem, a purchase? Something that only says one of the things above again does not count. Passing things of today never count: what {self} is doing, reading, eating, or watching right now, the weather, a noise, a mood, being busy or tired, and jokes about these.{side}

Answer with JSON only: {{"tells": [true or false for each thing, in order], "other": "" or a short description of the other fact}}"""


def flat(checks):
    for c in checks:
        yield c
        yield from flat(c.get("any_of") or [])


def post(url, model, key, prompt, temperature):
    body = {"model": model, "messages": [{"role": "user", "content": prompt}], "max_tokens": 4000, "temperature": temperature}
    req = urllib.request.Request(url.rstrip("/") + "/chat/completions", data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json", **({"Authorization": "Bearer " + key} if key else {}),
                                          "User-Agent": "magi-evals-write3/1"})
    with urllib.request.urlopen(req, timeout=300) as r:
        s = json.load(r)["choices"][0]["message"].get("content") or ""
    s = re.sub(r"(?s)<think>.*?</think>", "", s)
    m = re.search(r"(?s)\{.*\}", s)
    if not m:
        return {}
    try:
        v = json.loads(m.group(0))
    except json.JSONDecodeError:
        return {}
    return v if isinstance(v, dict) else {}


def valid(says, text, keep):
    low = text.lower()
    for k in keep:
        if not re.search(r"(?<![\w])" + re.escape(k.lower()) + r"(?![\w])", low):
            return "lacks " + k
    if "<<" in text or "{" in text or not 10 <= len(text) <= 1500:
        return "form"
    if set(re.findall(r"\d+", text)) - set(re.findall(r"\d+", says)):
        return "new number"
    return ""


def one(spec, a, keys):
    says = " ".join(p["say"] for p in spec["parts"])
    keep = paraphrase.keep_words({"text": says, "graph": list(flat(spec["graph"]))})
    # Only whole words of the parts ("Volvo XC" of "Volvo XC40" is not one).
    keep = [k for k in keep if re.search(r"(?<![\w])" + re.escape(k.lower()) + r"(?![\w])", says.lower())]
    lines = "\n".join(f"{i + 1}. \"{p['say']}\" ({p['meaning']})" if p.get("meaning") else f"{i + 1}. \"{p['say']}\""
                      for i, p in enumerate(spec["parts"]))
    topics = random.Random(spec["id"]).sample(TOPICS, random.Random(spec["id"] + "n").randint(2, 4))
    side = "" if spec["noise"] == "none" else " The message has side remarks about " + ", ".join(topics) + "; they do not count."
    prompt = WRITE.format(self=spec["self"], parts=lines, style=spec["style"], noise=paraphrase_noise(spec["noise"], topics),
                          keep=", ".join(f'"{x}"' for x in keep) or "(nothing)")
    why = []
    for _ in range(a.tries):
        try:
            text = str(post(a.url, a.model, keys[0], prompt, 1.0).get("message") or "").strip()
        except Exception as e:  # noqa: BLE001 - a failed call counts as a failed try
            why.append(f"write {type(e).__name__} {getattr(e, 'code', '')}")
            continue
        bad = valid(says, text, keep)
        if bad:
            why.append(bad)
            continue
        try:
            v = post(a.check_url, a.check_model, keys[1], CHECK.format(text=text, parts=lines, self=spec["self"], side=side), 0.0)
        except Exception as e:  # noqa: BLE001
            why.append(f"check {type(e).__name__} {getattr(e, 'code', '')}")
            continue
        tells = v.get("tells")
        other = str(v.get("other") or "").strip()
        if not isinstance(tells, list) or len(tells) != len(spec["parts"]) or not all(x is True for x in tells):
            why.append(f"check tells {tells}")
            continue
        if other and other.lower() not in ("none", "no", "n/a", "\"\""):
            why.append("check other: " + other[:80])
            continue
        c = dict(spec)
        c["text"] = text
        return c, why
    return None, why


# Topics of side remarks: passing things, no lasting facts.
TOPICS = ["the weather", "a cold coffee", "a slow laptop", "a song stuck in your head", "a long line at the store", "a podcast",
          "the neighbor's leaf blower", "burnt toast", "a funny meme", "a very loud dishwasher", "a sunset",
          "too many emails", "a nap you want", "a sports game on TV", "a spilled drink", "lunch", "a tiring meeting", "the wind",
          "a good book chapter", "a cat video", "a parking spot", "a grocery run", "the heat", "a bad night of sleep",
          "the news", "a crossword", "cleaning the kitchen", "the laundry", "a muddy walk", "a slow bus", "the snow", "a smoothie",
          "the gym being busy", "a video call that froze", "a long hold on the phone", "a pizza", "a rainy walk", "a quiet morning"]


def paraphrase_noise(k, topics):
    t = ", ".join(topics)
    return {
        "none": "Tell only the things above, nothing more.",
        "light": f"Add one or two short side remarks, for example about {t}.",
        "heavy": (f"Ramble: add several side remarks and small tangents before, between, and after the things, for example about {t}. "
                  "The things can be in the middle of the talk."),
    }[k]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("inputs", nargs="+")
    ap.add_argument("--out", required=True)
    ap.add_argument("--url", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--key-file", default="")
    ap.add_argument("--check-url", required=True)
    ap.add_argument("--check-model", required=True)
    ap.add_argument("--check-key-file", default="")
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--tries", type=int, default=3)
    ap.add_argument("--per-file", type=int, default=500)
    ap.add_argument("--prefix", default="cases")
    ap.add_argument("--log", default="", help="file for the reasons of failed tries")
    a = ap.parse_args()
    keys = [Path(f).read_text().strip() if f else "" for f in (a.key_file, a.check_key_file)]
    specs = []
    for pat in a.inputs:
        for f in sorted(glob.glob(pat)):
            specs += [json.loads(l) for l in open(f) if l.strip()]
    results, failed, done = {}, 0, 0
    log = open(a.log, "w") if a.log else None
    with cf.ThreadPoolExecutor(a.workers) as ex:
        futs = {ex.submit(one, s, a, keys): s["id"] for s in specs}
        for fu in cf.as_completed(futs):
            c, why = fu.result()
            results[futs[fu]] = c
            failed += c is None
            done += 1
            if log:
                log.write(json.dumps({"id": futs[fu], "ok": c is not None, "why": why}) + "\n")
                log.flush()
            if done % 50 == 0:
                print(f"{done}/{len(specs)} failed {failed}", file=sys.stderr, flush=True)
    rows = [results[s["id"]] for s in specs if results.get(s["id"])]
    od = Path(a.out)
    od.mkdir(parents=True, exist_ok=True)
    for i in range(0, len(rows), a.per_file):
        with open(od / f"{a.prefix}-{i // a.per_file:03d}.jsonl", "w") as f:
            for c in rows[i: i + a.per_file]:
                f.write(json.dumps(c, ensure_ascii=False) + "\n")
    print(f"{len(rows)} cases of {len(specs)} specs ({failed} with none)")


if __name__ == "__main__":
    main()
