"""Score the knowledge graph of a person against the truth of world-v2
(world.py), through the magi command (`magi know -past TEXT`).

  python3 -I evals/world/graph_check.py --magi PATH [--json OUT]

Each truth is a search text, regular expressions that a current fact must
match (all of them, in one line or several), and regular expressions that no
current fact may match. A fact line ends with "(ended)" when it is no
longer true. This checks what the note taker kept, apart from the persona.
"""

import argparse
import json
import re
import subprocess
import sys

# (id, search text, must be current, must not be current)
TRUTH = [
    ("home", "where do I live", [r"lives.*zuni"], [r"lives.*pine street"]),
    ("employer", "where do I work", [r"works-at juniper"], [r"jamie works-at orbital", r"works-at engineering lead"]),
    ("job", "my job title", [r"jamie (job|title|role|position) .*engineering lead"], [r"jamie job orbital"]),
    ("manager", "my manager", [r"jamie manager rafael"], [r"jamie manager teodora"]),
    ("car", "my car", [r"ev9"], [r"jamie car .*mazda"]),
    ("dentist", "my dentist", [r"dentist .*shaw"], [r"jamie dentist .*(pearl|cherry)"]),
    ("vet", "Pepper's vet", [r"pepper vet .*malik"], [r"pepper vet .*ito"]),
    ("book club", "book club where it meets", [r"bookbar"], [r"(meets|meets-at|located|at) .*tattered"]),
    ("coach", "Ruby's soccer coach", [r"coach .*jen parks"], [r"coach jen park$"]),
    ("team", "Ruby soccer team", [r"ruby (team|plays-for|plays-on) .*u12"], []),
    ("harbor", "Harbor launch due date", [r"harbor.*(january 15|2027-01-15|jan 15)"], [r"harbor.*(due|deadline|date) .*december 1\b"]),
    ("budget", "kitchen remodel budget", [r"budget .*44"], [r"budget .*38,000$"]),
    ("sven", "who reports to me Sven", [r"sven holt (manager|reports-to) jamie|jamie (report|direct-report|team-member|team) .*sven"], []),
    ("bea", "who reports to me Bea", [r"bea moreau (manager|reports-to) jamie|jamie (report|direct-report|team-member|team) .*bea"], [r"jamie manager bea"]),
    ("nadia", "Nadia reports to me", [], [r"nadia kerr (manager|reports-to) jamie"]),
    ("alex wfh", "Alex works from home", [r"alex .*(monday|mondays)"], [r"alex works-at home$"]),
    ("wifi", "home wifi network name", [r"larkspur"], []),
    ("insurance", "home and auto insurance", [r"front range mutual"], []),
    ("agent", "insurance agent", [r"doug keller"], []),
    ("married", "wedding anniversary married", [r"(2014|september 27)"], []),
    ("ruby birthday", "Ruby's birthday", [r"ruby birthday .*september 3, 2016"], []),
    ("theo birthday", "Theo's birthday", [r"theo birthday .*february 21, 2019"], []),
    ("my birthday", "my birthday", [r"jamie birthday .*april 2, 1987"], [r"jamie birthday 37"]),
    ("climbing", "climbing gym", [], [r"jamie climbs-at movement"]),
    ("pt", "physical therapy", [r"corinne duval"], []),
    ("wen", "Wen Li build system", [r"wen li"], [r"wen li .*data team"]),
    ("teodora", "Teodora Vance works at", [r"teodora vance works-at orbital"], []),
    ("restaurant", "restaurant in Porto", [r"avillez"], []),
    ("priya", "Priya Raman", [], [r"priya raman manager jamie", r"jamie manager priya"]),
    ("marco", "Marco Mazda", [], [r"jamie car .*cx-5.*marco|marco.*(owns|bought) .*jamie"]),
]


def know(magi, text):
    p = subprocess.run([magi, "know", "-past", text], capture_output=True, text=True, timeout=120)
    lines = []
    for raw in p.stdout.splitlines():
        m = re.match(r"^\s+clm_\S+\s+(.*?)\s*$", raw)
        if m:
            fact = m.group(1)
            ended = fact.endswith("(ended)")
            fact = re.sub(r"\s*\((ended|since [^)]*)\)\s*$", "", fact).strip().lower()
            lines.append((fact, ended))
    return lines


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--magi", default="magi")
    ap.add_argument("--json", default="")
    a = ap.parse_args()
    out, ok_n = [], 0
    for tid, text, must, never in TRUTH:
        facts = know(a.magi, text)
        current = [f for f, ended in facts if not ended]
        missing = [r for r in must if not any(re.search(r, f) for f in current)]
        wrong = [f for f in current for r in never if re.search(r, f)]
        ok = not missing and not wrong
        ok_n += ok
        out.append({"id": tid, "ok": ok, "missing": missing, "wrong": wrong})
        print(f"{'PASS' if ok else 'FAIL'} {tid:14} {'missing ' + str(missing) if missing else ''}{' wrong ' + str(wrong) if wrong else ''}")
    print(f"{ok_n}/{len(TRUTH)} truths")
    if a.json:
        with open(a.json, "w") as f:
            json.dump({"score": f"{ok_n}/{len(TRUTH)}", "items": out}, f, indent=1)


if __name__ == "__main__":
    sys.exit(main())
