#!/usr/bin/env python3
"""Architecture rules as a check. tools/check.sh runs this first.

The rules are those of CLAUDE.md and docs/conventions.md. A rule that is
only prose is broken without notice; a rule here fails the check.

Rules:
  imports   A primitive imports only its own module and lib/. Its cmd/
            packages can also import the generated api client of other
            primitives. lib/ imports neither a primitive nor magi/. Only
            magi/ composes primitives.
  private   No private name (host, account, domain) in a file. The names
            are in tools/arch-private.sha256 as SHA-256 values, so that
            the list does not name them.
  opal      Nothing has the name "opal".
  node      No Node.js project and no JavaScript out of the web app.

Known breaks are in tools/arch-baseline.txt, one "rule<TAB>path<TAB>key"
line each. The baseline only gets smaller: a new break fails the check,
and so does a line whose break is gone (delete the line).

  tools/arch-check.py                 check
  tools/arch-check.py --hash NAME     print the line to add for a private name
  tools/arch-check.py --write-baseline  write the baseline from the breaks now
"""
import hashlib
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASELINE = "tools/arch-baseline.txt"
PRIVATE = "tools/arch-private.sha256"
MODULE = "github.com/catalystcommunity/magi/"

# Paths that are never committed, and files that state a rule by name.
SKIP_PREFIX = ("evals/coding/results/", "magi/magi-world", "go.work.sum", ".claude/", ".git/")
OPAL_EXEMPT = {"CLAUDE.md", "AGENTS.md", "docs/conventions.md", "tools/arch-check.py", "tools/arch_check_test.py"}
JS_ALLOWED = ("magi/web/", "clients/typescript/")
JS_SUFFIX = (".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx")
MAX_BYTES = 4 << 20

IMPORT_RE = re.compile(r'"(' + re.escape(MODULE) + r'[^"]+)"')
API_RE = re.compile(re.escape(MODULE) + r"primitives/[^/]+/api$")
LABEL_RE = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")
OPAL_RE = re.compile(r"(?<![A-Za-z])[Oo][Pp][Aa][Ll](?![a-z])")


def files(root):
    """Tracked files and new files that git does not ignore."""
    try:
        out = subprocess.run(
            ["git", "ls-files", "-co", "--exclude-standard", "-z"],
            cwd=root, capture_output=True, check=True).stdout.decode()
        names = [n for n in out.split("\0") if n]
    except (OSError, subprocess.CalledProcessError):
        names = []
        for d, dirs, fs in os.walk(root):
            dirs[:] = [x for x in dirs if x not in (".git", "node_modules", "target")]
            names += [os.path.relpath(os.path.join(d, f), root) for f in fs]
    return sorted(n for n in names
                  if not n.startswith(SKIP_PREFIX) and os.path.isfile(os.path.join(root, n)))


def read_text(path):
    try:
        if os.path.getsize(path) > MAX_BYTES:
            return None
        with open(path, "rb") as f:
            b = f.read()
    except OSError:
        return None
    if b"\0" in b:
        return None
    return b.decode("utf-8", "replace")


def sha(name):
    return hashlib.sha256(name.lower().encode()).hexdigest()


def check_imports(path, text):
    """Yield (key, line number) for each import that the rule forbids."""
    if not path.endswith(".go"):
        return
    parts = path.split("/")
    # A cmd/ package is a program, not the library of the primitive: it can
    # call other services through their generated clients.
    client_ok = parts[0] == "primitives" and len(parts) > 3 and parts[2] == "cmd"
    if parts[0] == "primitives" and len(parts) > 2:
        own = MODULE + "primitives/" + parts[1]
        allowed = (own + "/", MODULE + "lib/")
    elif parts[0] == "lib" and len(parts) > 2:
        own = MODULE + "lib/" + parts[1]
        allowed = (MODULE + "lib/",)
    else:
        return
    for n, line in enumerate(text.splitlines(), 1):
        for imp in IMPORT_RE.findall(line):
            if imp == own or imp.startswith(allowed) or (client_ok and API_RE.match(imp)):
                continue
            yield imp[len(MODULE):], n


def check_private(path, text, hashes):
    if path == PRIVATE or not hashes:
        return
    seen = {}
    for n, line in enumerate(text.lower().splitlines(), 1):
        for label in LABEL_RE.findall(line):
            if len(label) >= 3 and label not in seen:
                seen[label] = n
    for label, n in seen.items():
        h = sha(label)
        if h in hashes:
            yield h[:12], n


def check_opal(path, text):
    if path in OPAL_EXEMPT:
        return
    if OPAL_RE.search(os.path.basename(path)):
        yield "name", 0
    for n, line in enumerate(text.splitlines(), 1):
        if OPAL_RE.search(line):
            yield "text", n
            return


def check_node(path):
    base = os.path.basename(path)
    if base in ("package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml") \
            and not path.startswith("magi/web/"):
        yield base, 0
    elif path.endswith(JS_SUFFIX) and not path.startswith(JS_ALLOWED):
        yield "javascript", 0


def load_lines(root, rel):
    try:
        with open(os.path.join(root, rel)) as f:
            return [l.rstrip("\n") for l in f if l.strip() and not l.startswith("#")]
    except OSError:
        return []


def find(root):
    """Return {(rule, path, key): line number} for each break in root."""
    hashes = {l.split()[0] for l in load_lines(root, PRIVATE)}
    found = {}
    for path in files(root):
        for key, n in check_node(path):
            found.setdefault(("node", path, key), n)
        text = read_text(os.path.join(root, path))
        if text is None:
            continue
        for key, n in check_imports(path, text):
            found.setdefault(("imports", path, key), n)
        for key, n in check_private(path, text, hashes):
            found.setdefault(("private", path, key), n)
        for key, n in check_opal(path, text):
            found.setdefault(("opal", path, key), n)
    return found


HELP = {
    "imports": "a primitive imports only its own module and lib/ (cmd/ also the api client of other primitives); move the composition to magi/",
    "private": "a private name is in this file; use example.local or a role name",
    "opal": 'nothing has the name "opal"',
    "node": "no Node.js project and no JavaScript out of the web app",
}


def main(argv):
    root = ROOT
    if "--root" in argv:
        root = os.path.abspath(argv[argv.index("--root") + 1])
    if "--hash" in argv:
        print(sha(argv[argv.index("--hash") + 1]))
        return 0
    found = find(root)
    if "--write-baseline" in argv:
        with open(os.path.join(root, BASELINE), "w") as f:
            f.write("# Known breaks of the architecture rules (tools/arch-check.py).\n"
                    "# This file only gets smaller. Do not add a line; correct the break.\n")
            for k in sorted(found):
                f.write("\t".join(k) + "\n")
        print(f"wrote {len(found)} lines to {BASELINE}")
        return 0
    base = {tuple(l.split("\t")) for l in load_lines(root, BASELINE)}
    new = sorted(k for k in found if k not in base)
    stale = sorted(k for k in base if k not in found)
    for rule, path, key in new:
        n = found[(rule, path, key)]
        where = f"{path}:{n}" if n else path
        print(f"arch-check: {rule}: {where}: {key}: {HELP.get(rule, '')}")
    for k in stale:
        print(f"arch-check: baseline: the break is gone; delete this line from {BASELINE}: " + " ".join(k))
    if new or stale:
        print(f"arch-check: FAILED ({len(new)} new, {len(stale)} stale baseline lines)")
        return 1
    print(f"arch-check: ok ({len(base)} known breaks in the baseline)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
