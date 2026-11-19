"""Spec step: write acceptance checks for each case with a model.

The model reads the prompt of spec-step.md and the instructions of one
case, and writes the checks. run.py --checks-dir adds them to the task.

usage: python3 spec_step.py --url https://magi.example.local/runner/v1 \\
         --token-file runner.token --model NAME --out checks/NAME
"""
import argparse
import json
import os
import time
import urllib.request


def main(argv=None):
    here = os.path.dirname(os.path.abspath(__file__))
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--url", required=True, help="an OpenAI-compatible base URL")
    ap.add_argument("--token-file", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--temperature", type=float, default=0.3)
    args = ap.parse_args(argv)
    with open(os.path.join(here, "spec-step.md")) as f:
        system = f.read()
    with open(args.token_file) as f:
        token = f.read().strip()
    os.makedirs(args.out, exist_ok=True)
    cases = os.path.join(here, "cases")
    for name in sorted(os.listdir(cases)):
        with open(os.path.join(cases, name)) as f:
            task = f.read()
        body = {"model": args.model, "temperature": args.temperature, "max_tokens": 12000,
                "messages": [{"role": "system", "content": system},
                             {"role": "user", "content": "Task:\n\n" + task}]}
        # A User-Agent: some proxies refuse the default of urllib.
        req = urllib.request.Request(args.url.rstrip("/") + "/chat/completions", json.dumps(body).encode(),
                                     {"Content-Type": "application/json", "Authorization": "Bearer " + token,
                                      "User-Agent": "magi-evals/1"})
        t = time.time()
        with urllib.request.urlopen(req, timeout=1800) as r:
            text = (json.load(r)["choices"][0]["message"].get("content") or "").strip()
        with open(os.path.join(args.out, name), "w") as f:
            f.write(text + "\n")
        print("%s: %d s, %d chars" % (name, time.time() - t, len(text)), flush=True)


if __name__ == "__main__":
    main()
