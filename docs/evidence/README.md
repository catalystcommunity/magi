# Retained test evidence

The [archive index](index.md) identifies selected reports from the development
tests. The full private archive contains 676 files. It includes failed tests,
successful tests, deployment records, and response bytes. No raw report was
deleted during this change.

These records describe past tests. They do not establish the current state of
a service. A filename or a hash alone does not establish a successful test.

## Milestones

- The [conversation demo](../conversation-demo.md) records text, speech, and
  parallel task tests. These tests do not establish physical speaker identity.
- The [release tests](../release-demo.md) record human approval, promotion,
  automatic rollback, and recovery after a controller restart. Their scope is
  the isolated stateless canary, not a general database rollback.
- The [task proof](index.md#evidence-050) connects a durable task with its
  release result. It does not give a task worker promotion authority.
- The rename checks have separate [core](index.md#evidence-056),
  [release](index.md#evidence-057), and [demo](index.md#evidence-058) records.

## Storage

New reports go to ignored `test-results/` and experiment `results/` directories.
The [experiment index](experiments.md) identifies archived experiment reports.
Tests must not depend
on reports from a previous run. Public test inputs belong in test fixtures.

Current deployment inputs go to `~/data/magi-evidence/deployments/default/`. These files
are private operator state, not disposable test output. See the
[configuration guide](../../config/README.md).

The current private archive pointer is `~/data/magi-evidence/publication-archive.json`.
The earlier root-only archive also has a copy under that external directory.
The pointer identifies the archive and its checksum manifest. Keep a separate
backup on another host. Review report contents before sharing any raw file.
The public index contains only filenames, sizes, and hashes.
