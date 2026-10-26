# Public repository checklist

Public source includes code, design documents, synthetic test fixtures, and
dependency or model pins. Private state includes operator policy, credentials,
runtime databases, raw logs, transcripts, voice recordings, and user reviews.

## Check the files

Run these checks before staging files:

```sh
npm run check:publication
git status --short
git diff --check
```

The publication check reads tracked files and non-ignored working files. It
reports paths and rule names, not secret values. It rejects private storage
paths, raw report formats, non-example configuration, private keys, private
JSON Web Keys, and selected token formats. It does not detect every secret or
personal statement. Review the proposed diff as well.

After you stage the intended files, check the exact index bytes:

```sh
npm run check:publication -- --staged
git diff --cached --check
git diff --cached --stat
```

The staged check includes files added with an ignore-rule override. It does
not stage, commit, or push anything. A check of an empty index checks no files.
Do not force-add ignored data to resolve a missing test input.

## Private storage

Operator records use the overlay's `operatorStateDir`. The example uses
`~/data/magi-evidence/deployments/default/`. The absolute
`MAGI_OPERATOR_STATE_DIR` override takes precedence. Archives use
`~/data/magi-evidence/archives/`. See the [configuration guide](../config/README.md).
Operator profiles and environment settings use `~/.config/magi/`. Local
synthetic development state can remain under ignored `.local/` paths.

Raw root and experiment reports are ignored. Earlier reports have moved to
the private archive outside the checkout. Public documents retain
result summaries and [archive indexes](evidence/README.md). Tests must create
their own output and use synthetic inputs.

## Release decisions

Magi uses the [Apache License, Version 2.0](../LICENSE). Retain the project
license and the existing third-party license notices when preparing a release.

Public examples use `catalystsquad.local`. Actual deployment limits come from
the private overlay. Check for accidental copies of those values before staging:

```sh
npm run check:publication -- --operator-config "$HOME/.config/magi/deployment.json"
```

Add `--staged` to check index bytes after staging. This comparison reports
filenames and rule names, not private values. Also inspect the diff for other
environment details that the overlay does not contain.
If the overlay names an existing release policy, the check also compares its
approver domain and account ID. It does not print them.

The vendored LinkKeys SDK has two public example comment changes. The vendor
script preserves these changes and checks them with `--check`. SDK behavior
and license notices are unchanged by these comment changes.

Some source-map links refer to sibling ecosystem checkouts. Local tests need
the pinned build tools and the documented ecosystem dependencies. The current
publication checks do not replace a separate setup test on a new machine.
