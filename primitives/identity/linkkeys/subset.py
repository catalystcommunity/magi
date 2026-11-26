#!/usr/bin/env python3
"""Makes rp.csil: the part of the LinkKeys contract that Magi uses.

  python3 -I subset.py PATH/linkkeys.csil > rp.csil

The rules are copied from the upstream file with no change, in the order
of that file: the operations in OPS, and each rule that they need. A hand
copy of the rules went out of date when LinkKeys changed its types. Run
this for each new LinkKeys version, then gen.sh.
"""
import re
import sys

# The LinkKeys release that rp.csil is from: the tag of the image.
VERSION = "0.16.12"
# The operations of the Rp service that a regular relying party needs for
# a browser login.
OPS = ["sign-request", "decrypt-token", "verify-assertion", "userinfo-fetch", "resolve-domain-keys"]

HEADER = f""";;; The part of the LinkKeys {VERSION} contract (linkkeys/csil/linkkeys.csil)
;;; that a regular relying party needs for a browser login. subset.py makes
;;; this file from the upstream contract. Do not edit it by hand.

options {{
  go_package: "linkkeys",
}}
"""


def rules(text):
    """Returns the top-level rules in order: [(name, source with its comments)]."""
    out, lines, i = [], text.split("\n"), 0
    while i < len(lines):
        m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*) = ", lines[i])
        if not m:
            i += 1
            continue
        start = i
        while start > 0 and lines[start - 1].startswith(";;") and "====" not in lines[start - 1]:
            start -= 1
        depth, j = 0, i
        while True:
            code = lines[j].split(";;")[0]
            depth += sum(code.count(c) for c in "{([") - sum(code.count(c) for c in "})]")
            if depth <= 0:
                break
            j += 1
        out.append((m.group(1), "\n".join(lines[start:j + 1])))
        i = j + 1
    return out


def main():
    text = open(sys.argv[1]).read()
    all_rules = rules(text)
    by_name = dict(all_rules)
    svc = re.search(r"^service Rp \{(.*?)^\}", text, re.S | re.M).group(1)
    ops, need = [], []
    for line in svc.split("\n"):
        m = re.match(r"\s*([a-z-]+):\s*(.+?),?\s*$", line.split(";;")[0])
        if m and m.group(1) in OPS:
            ops.append("  " + m.group(1) + ": " + m.group(2) + ",")
            need += re.findall(r"[A-Za-z_][A-Za-z0-9_]*", m.group(2))
    missing = set(OPS) - {o.strip().split(":")[0] for o in ops}
    if missing:
        sys.exit(f"the Rp service has no operation {sorted(missing)}")
    seen = set()
    while need:
        n = need.pop()
        if n in seen or n not in by_name:
            continue
        seen.add(n)
        body = "\n".join(l.split(";;")[0] for l in by_name[n].split("\n"))
        need += re.findall(r"[A-Za-z_][A-Za-z0-9_]*", body.split("=", 1)[1])
    print(HEADER)
    for name, src in all_rules:
        if name in seen:
            print(src + "\n")
    print("service Rp {\n" + "\n".join(ops) + "\n}")


if __name__ == "__main__":
    main()
