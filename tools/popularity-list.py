#!/usr/bin/env python3
"""Writes the popularity list of the automatic destination decision.

  python3 -I tools/popularity-list.py TOP-1M.CSV.ZIP

The input is a list of the 1,000,000 most visited domains, one
"rank,domain" in each line, in a zip file (the Tranco list,
https://tranco-list.eu). The output is magi/compose/popularity/: one gzip
file for each 50,000 ranks, so that each file is small. The decision model
was trained with one list. Measure the model again (evals/egress) after a
change of the list.
"""
import gzip
import io
import os
import sys
import zipfile

BUCKET = 50000


def main():
    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "magi", "compose", "popularity")
    z = zipfile.ZipFile(sys.argv[1])
    lines = [l.strip() for l in io.TextIOWrapper(z.open(z.namelist()[0]), encoding="utf-8") if l.strip()]
    for f in os.listdir(out):
        if f.endswith(".csv.gz"):
            os.remove(os.path.join(out, f))
    for i in range(0, len(lines), BUCKET):
        part = lines[i:i + BUCKET]
        name = f"{i + 1:07d}-{i + len(part):07d}.csv.gz"
        with open(os.path.join(out, name), "wb") as f:
            # mtime 0: the same list gives the same bytes.
            with gzip.GzipFile(fileobj=f, mode="wb", mtime=0, filename="") as g:
                g.write(("\n".join(part) + "\n").encode())
    print(len(lines), "domains in", (len(lines) + BUCKET - 1) // BUCKET, "files")


if __name__ == "__main__":
    main()
