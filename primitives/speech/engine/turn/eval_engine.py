"""Send the held-out set to a running turn engine and print the metrics
and the time of each call. Standard library only.

  python eval_engine.py URL TOKEN_FILE [--ack 0.85] [--turn 0.5] [--set 2]

A decision is "ack" when P(ack) >= --ack, "turn" when P(turn) >= --turn,
else "wait" (more words are necessary).
"""

import json
import sys
import time
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import heldout  # noqa: E402
import heldout2  # noqa: E402


def main():
    url, token_file = sys.argv[1].rstrip("/"), sys.argv[2]
    args = sys.argv[3:]
    t_ack = float(args[args.index("--ack") + 1]) if "--ack" in args else 0.85
    t_turn = float(args[args.index("--turn") + 1]) if "--turn" in args else 0.5
    mod = heldout2 if "--set" in args and args[args.index("--set") + 1] == "2" else heldout
    token = Path(token_file).read_text().strip()
    rows = mod.make()
    times, decided = [], []
    for r in rows:
        body = json.dumps({"items": [{"context": r["context"], "text": r["text"]}]}).encode()
        req = urllib.request.Request(url + "/turn", body, {"Content-Type": "application/json",
                                                           "Authorization": "Bearer " + token})
        t0 = time.perf_counter()
        with urllib.request.urlopen(req, timeout=10) as resp:
            res = json.load(resp)
        times.append((time.perf_counter() - t0) * 1000)
        p = res["results"][0]
        d = "ack" if p["ack"] >= t_ack else "turn" if p["turn"] >= t_turn else "wait"
        decided.append((r, d, p))
    turns = [x for x in decided if x[0]["label"] == "turn"]
    acks = [x for x in decided if x[0]["label"] == "ack"]
    print(json.dumps({
        "items": len(rows),
        "turns_taken_as_ack": [x[0]["text"] for x in turns if x[1] == "ack"],
        "turns_stopped": sum(x[1] == "turn" for x in turns), "turns": len(turns),
        "acks_ignored": sum(x[1] == "ack" for x in acks), "acks": len(acks),
        "acks_that_stop": [x[0]["text"] for x in acks if x[1] == "turn"],
        "ms_median": round(sorted(times)[len(times) // 2], 1), "ms_max": round(max(times), 1),
    }, indent=1))


if __name__ == "__main__":
    main()
