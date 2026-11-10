"""Score a benchmark result only on the test cases whose text is not in the training cases.

  python3 -I evals/notetaker/heldout.py RESULT.jsonl [RESULT.jsonl ...]

A case is held out when its text (lowercase, spaces collapsed) is not the
text of any case in train/cases-*.jsonl. This is the honest score of an
adapter trained on those cases.
"""

import collections
import glob
import json
import sys
from pathlib import Path

HERE = Path(__file__).parent


def norm(s):
    return " ".join(s.lower().split())


def main():
    seen = {norm(json.loads(l)["text"]) for f in glob.glob(str(HERE / "train" / "cases-*.jsonl")) for l in open(f)}
    test = {}
    for f in sorted(glob.glob(str(HERE / "data" / "test-*.jsonl"))):
        for l in open(f):
            c = json.loads(l)
            test[c["id"]] = norm(c["text"]) not in seen
    held = sum(test.values())
    print(f"held-out cases: {held}/{len(test)}")
    for path in sys.argv[1:]:
        cat = collections.defaultdict(lambda: [0, 0])
        ok = n = 0
        for l in open(path):
            r = json.loads(l)
            if not test.get(r["id"]):
                continue
            n += 1
            ok += bool(r.get("ok"))
            cat[r["category"]][0] += bool(r.get("ok"))
            cat[r["category"]][1] += 1
        print(f"{Path(path).stem}: {ok}/{n} held out ({100 * ok // max(n, 1)}%)")
        for k in sorted(cat):
            print(f"  {k:16} {cat[k][0]}/{cat[k][1]}")


if __name__ == "__main__":
    main()
