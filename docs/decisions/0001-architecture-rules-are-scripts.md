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

The baseline has 12 lines for a private name in the eval data. The
correction of those lines needs a new version of the eval set.

Decided on 2026-11-20: the token renewer is `lib/authn/renew`. It gets the
refresh call from its caller. A `cmd/` package of a primitive can import
the generated `api` client of a different primitive, and no other package
of it. The import break is gone from the baseline.
