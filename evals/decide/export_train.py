"""Training records for a decision LoRA (README.md, "Training").

  python3 -I evals/decide/export_train.py CASES_GLOB --out DIR [--skip count] [--per-file N] [--seed S]

Each question of eval_notes.py on each case becomes one chat record: the
prompt of decide.py (system and user) and the label as the answer. This is
the format of `notes-eval -export`, so train_lora.py of evals/notetaker
trains on it. The answer is one token, so the loss is on that token.

--format gemma4: plain prompt and completion text, with the prompt as
llama-server renders it with thinking off (it ends with an empty thought
channel). The Hugging Face template of Gemma 4 removes that channel from
an answer, so chat records cannot give the prompt of decide.
"""

import argparse
import glob
import json
import random
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import decide as D  # noqa: E402
import eval_notes as E  # noqa: E402

GEMMA4 = "<bos><|turn>system\n{system}<turn|>\n<|turn>user\n{user}<turn|>\n<|turn>model\n<|channel>thought\n<channel|>"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("cases")
    ap.add_argument("--out", required=True)
    ap.add_argument("--skip", default="count", help="question ids to leave out, comma-separated")
    ap.add_argument("--per-file", type=int, default=5000)
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--format", default="chat", choices=["chat", "gemma4"])
    a = ap.parse_args()
    skip = set(filter(None, a.skip.split(",")))
    recs = []
    for f in sorted(glob.glob(a.cases)):
        for line in open(f):
            if not line.strip():
                continue
            c = json.loads(line)
            st = E.state_of(c)
            for q, truth in E.questions(c):
                if q["id"] in skip:
                    continue
                if a.format == "gemma4":
                    recs.append({"prompt": GEMMA4.format(system=D.SYSTEM, user=D.user_prompt(st, q)), "completion": truth + "<turn|>"})
                else:
                    recs.append({"messages": [{"role": "system", "content": D.SYSTEM},
                                              {"role": "user", "content": D.user_prompt(st, q)},
                                              {"role": "assistant", "content": truth}]})
    random.Random(a.seed).shuffle(recs)
    out = Path(a.out)
    out.mkdir(parents=True, exist_ok=True)
    for i in range(0, len(recs), a.per_file):
        with open(out / f"train-{i // a.per_file:03d}.jsonl", "w") as f:
            for r in recs[i:i + a.per_file]:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
    print(len(recs), "records")


if __name__ == "__main__":
    main()
