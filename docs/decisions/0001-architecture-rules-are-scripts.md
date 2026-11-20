# 0001 Architecture rules are scripts that fail the check

Status: done

## Decision

Each architecture rule that a program can test is a rule in
`tools/arch-check.py`. `tools/check.sh` runs it first. A break fails the
check.

Breaks that were there before the rule are in `tools/arch-baseline.txt`.
The baseline only gets smaller. A new break fails the check. A baseline
line whose break is gone also fails the check, so that the line is
deleted.

Private names are in `tools/arch-private.sha256` as SHA-256 values. The
list does not name them.

## Reason

A rule that is only prose is broken without notice. The code-change
workflow runs `tools/check.sh` after each change, so a rule in the check
also applies to changes that Magi makes to itself.

## Done when

- The rules for imports, private names, the forbidden name, and Node.js
  are in the script. Done.
- The baseline is empty.

## Open

`primitives/inference` imports `primitives/identity/renew` to renew the
token of an inference host. The line is in the baseline. The correction
needs a decision: a shared library package that gets the refresh call
from its caller, or a rule that permits the generated client of a
different primitive.
