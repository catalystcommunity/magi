"""A decision model from an open LLM (README.md).

A question has a type and options:

- noul: yes or no.
- choice: one of up to 26 options.
- score: a whole number from 0 to N (N <= 9).

The prompt gives the state, the question, and the options with one-token
labels (A, B, ... or 0, 1, ...). The model reads the prompt once and gives
the log-probabilities of its first token. The probabilities of the labels,
normalized over the labels only, are the answer. No text is generated.

All questions of one state have the same prompt prefix (system and state),
so llama-server uses its prompt cache after the first question.

A temperature for each question (calibrate.py) makes the probabilities
match how often the model is right.
"""

import json
import math
import string
import urllib.request

SYSTEM = ("You are a decision model. Read the state. Then answer the question with the label of the one option "
          "that is correct. Answer with the label only.")


def labels_for(q):
    if q["type"] == "score":
        return [str(i) for i in range(q.get("max", 4) + 1)]
    n = 2 if q["type"] == "noul" else len(q["options"])
    if n > 26:
        raise ValueError("at most 26 options")
    return list(string.ascii_uppercase[:n])


def option_lines(q):
    labels = labels_for(q)
    if q["type"] == "noul":
        texts = ["yes", "no"]
    elif q["type"] == "choice":
        texts = q["options"]
    else:
        texts = [q.get("scale", {}).get(l, "") for l in labels]
    return "\n".join(f"{l}: {t}" if t else l for l, t in zip(labels, texts))


def user_prompt(state, q):
    kind = {"noul": "one letter", "choice": "one letter", "score": "one number"}[q["type"]]
    return f"State:\n<<<\n{state}\n>>>\n\nQuestion: {q['question']}\nOptions:\n{option_lines(q)}\n\nAnswer with {kind}."


class Decider:
    def __init__(self, url, n_probs=200, timeout=120):
        self.url = url.rstrip("/")
        self.n_probs = n_probs
        self.timeout = timeout
        self._tok = {}
        self._tmpl = {}

    def _post(self, path, body):
        req = urllib.request.Request(self.url + path, data=json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json", "User-Agent": "magi-evals-decide/1"})
        with urllib.request.urlopen(req, timeout=self.timeout) as r:
            return json.load(r)

    def token_id(self, label):
        """The one token of a label; an error if the label is not one token."""
        if label not in self._tok:
            ids = self._post("/tokenize", {"content": label, "add_special": False})["tokens"]
            if len(ids) != 1:
                raise ValueError(f"label {label!r} is {len(ids)} tokens")
            self._tok[label] = ids[0]
        return self._tok[label]

    def prompt(self, state, q):
        msgs = [{"role": "system", "content": SYSTEM}, {"role": "user", "content": user_prompt(state, q)}]
        return self._post("/apply-template", {"messages": msgs})["prompt"]

    def logprobs(self, state, q, slot=None):
        """The log-probabilities of the labels (not normalized) and the time of the call.
        slot: a llama-server slot; the questions of one state on one slot use the prompt cache."""
        labels = labels_for(q)
        ids = {self.token_id(l): l for l in labels}
        body = {"prompt": self.prompt(state, q), "n_predict": 1, "n_probs": self.n_probs, "temperature": 0, "cache_prompt": True}
        if slot is not None:
            body["id_slot"] = slot
        d = self._post("/completion", body)
        top = d["completion_probabilities"][0]["top_logprobs"]
        floor = min(t["logprob"] for t in top) - 1.0
        got = {ids[t["id"]]: t["logprob"] for t in top if t["id"] in ids}
        return [got.get(l, floor) for l in labels], d.get("timings", {})

    def decide(self, state, questions, temps=None, slot=None):
        """Answers for each question: the label probabilities, the answer, and the confidence."""
        out = []
        for q in questions:
            lp, tm = self.logprobs(state, q, slot)
            t = (temps or {}).get(q.get("id", ""), 1.0)
            p = softmax([x / t for x in lp])
            labels = labels_for(q)
            best = max(range(len(p)), key=p.__getitem__)
            a = {"id": q.get("id", ""), "logprobs": lp, "probs": p, "ms": tm.get("prompt_ms", 0) + tm.get("predicted_ms", 0),
                 "cached": tm.get("cache_n", 0)}
            if q["type"] == "noul":
                a["answer"] = best == 0
            elif q["type"] == "choice":
                a["answer"] = q["options"][best]
            else:
                a["answer"] = int(labels[best])
                a["expected"] = sum(i * x for i, x in enumerate(p))
            a["confidence"] = p[best]
            out.append(a)
        return out


def softmax(xs):
    m = max(xs)
    e = [math.exp(x - m) for x in xs]
    s = sum(e)
    return [x / s for x in e]
