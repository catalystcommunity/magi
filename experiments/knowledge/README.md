# Knowledge storage experiment

This experiment tests the initial Magi knowledge records with synthetic data.
It includes Go and Rust CSIL codecs, a PostgreSQL store, and retrieval tests.
It is not an authenticated Magi service. Do not use it with private data.

The [retrieval comparison](retrieval/README.md) adds a separate small quality
corpus and local CPU models. It does not replace the original scale experiment.
The [support experiment](sufficiency/README.md) tests evidence selection.
The [capture package](capture/README.md) adds typed secret substitution and a
private local spool. Neither component exposes a public API.

Read the [results](../../docs/knowledge-experiment.md) and the
[storage proposal](../../docs/knowledge-storage.md) before extending the tests.

## Contents

| Path | Purpose |
| --- | --- |
| [schema/knowledge.csil](schema/knowledge.csil) | Experimental records, queries, errors, and service IDs |
| [toolchain.json](toolchain.json) | Compiler, generator, and PostgreSQL image pins |
| [generate.mjs](generate.mjs) | Verify pins and generate Go and Rust packages |
| [storage.sql](storage.sql) | Records, access fixtures, routes, receipts, and text index |
| [store.go](store.go) | Internal capture, admission, history, search, and publication operations |
| [storage_test.go](storage_test.go) | Privacy, retries, corrections, failure, and recovery checks |
| [codec_test.go](codec_test.go) | Exact types, optional fields, and codec checks |
| [schema/capture.csil](schema/capture.csil) | Secret bindings, four segment variants, batches, and receipts |
| [support.go](support.go) | Current-access boundaries and exact-citation validation |
| [capture](capture/README.md) | Streaming substitution, current-input preparation, and spool recovery |
| [retrieval_test.go](retrieval_test.go) | Deterministic corpus and measured source recall |
| [interop-rust/src/main.rs](interop-rust/src/main.rs) | Rust decoder and encoder used by Go tests |
| [run.mjs](run.mjs) | Test runner and retained result summaries |

## Requirements

Use a Linux host with Docker, Node.js, Go, Rust, Cargo, and the pinned csilgen
installation. The measured versions are in the results report. Other host
platforms are not tested.

The generator uses these default locations:

- Compiler: `~/.local/bin/csilgen`.
- Generators: `~/.csilgen/generators`.

Set `OPAL_CSILGEN` and `OPAL_CSIL_GENERATORS` to use other locations.
The script verifies the compiler version and generator hashes. It also verifies
the compiler hash on Linux x86-64. It copies the generators to a private
temporary directory. It does not change the shared installation.

The PostgreSQL image must already be in the local Docker cache. Its exact
digest is in `toolchain.json`. The harness uses `--pull=never`.
Go dependencies must be available from the module cache or the network.
Cargo builds use `--locked --offline`. If required, fetch the dependencies
while network access is available:

```sh
cargo fetch --locked --manifest-path interop-rust/Cargo.toml
```

## Run the checks

Run these commands from this directory.

To check that generated files match the pinned compiler and schema:

```sh
node generate.mjs --check
node generate.mjs --capture --check
```

To run the codec and storage checks with the Go race detector:

```sh
node run.mjs
```

This command skips the large retrieval corpus and records that skip.
It also skips the two model experiments, which have separate runners.
To include the corpus and repeat all checks three times:

```sh
node run.mjs --corpus --count 3
```

The runner creates unique JSONL logs and JSON summaries under `results/`.
Summaries contain test counts, source hashes, pins, and retrieval measurements.
The runner prints the summary when the tests finish. A successful exit means
that the executable regression checks passed. It does not mean that the
proposed recall target passed. Check `quality_target_met` in each measurement.

To regenerate files after a deliberate schema change:

```sh
node generate.mjs
```

Do not edit generated files. The script overwrites generated output files.
Keep handwritten code outside `generated/`. Review schema changes and repeat
both language checks before using new output.

Plain `go test ./...` does not run the full experiment. Storage checks require
`OPAL_KNOWLEDGE_INTEGRATION=1`. Rust checks require `OPAL_RUST_CODEC` to name
the built checker. Use `run.mjs` to set these values and avoid accidental skips.

## Database safety

Each storage run creates a new labelled container with synthetic credentials.
The container has a two-CPU limit and a 1 GiB memory limit. It binds only to
loopback. The harness selects an unused host port and fixes that port for the
container lifetime. A port conflict fails startup; it does not stop another
service.

The harness accepts no external database URL. It creates a separate schema for
each fixture. Runtime tests use a non-owner role without superuser or RLS
bypass privileges. Application code enforces fixture access rules; SQL row
security policies are not implemented.

The restart tests stop or kill only the new container. Cleanup checks its label
and removes its exact ID and anonymous volume. The synthetic database is then
unrecoverable. The fixture files and result logs remain, so the tests can rebuild
it. If the test process is forcibly stopped, inspect the recorded container ID
and label before manual cleanup. Never remove unrelated containers or volumes.

## Experiment boundaries

The six record variants preserve original input, interpretation, admitted
session input, presentation, assessment, and source observations. Delivery
receipt and actual context use are separate facts. A correction adds evidence;
it does not rewrite old input or repeat a tool action.

Fixture code supplies identities and grants. There is no public RPC listener,
LinkKeys login, signed allowance resolver, or production policy service.
Two authority namespaces share one disposable database. This is not federation.

The internal store accepts synthetic text that is already in archive reference
form. The separate capture package performs registered-value substitution;
it is not connected to authenticated evidence upload or the source store yet.
Opaque assessment extensions preserve bytes, but do not implement assessment
semantics. History traversal has a 128-record bound; cursor pagination is not
implemented. The experimental service declarations are not runnable endpoints.

The original scale run uses PostgreSQL full-text matching. It has no embeddings,
query model, live source adapter, object store, or speech path. Its corpus supplies exact
time windows. Its short text entries do not represent large media or long
agent histories. Do not use storage latency as the full interaction latency.

## Next Actions

1. Improve evidence sufficiency in a new evaluation version. The small model
   failed most support-mode checks. Preserve those results.
2. Add broader time windows and more distinct questions to test useful recall.
3. Complete W02 authentication before exposing any experiment operation.
4. Connect the tested capture primitives to admitted executors and remote upload
   before using real evidence.
