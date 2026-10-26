# Evidence capture results

Measured 2026-10-12 in America/Denver. The local prototype substitutes registered
credentials before durable storage. It preserves typed bindings and supports
current credential preparation without historical-value recovery.

The [full regression](evidence/experiments.md#experiment-024)
passed 237 leaf checks across three runs, with no failures or source drift.
The six deliberate skips cover the separate retrieval-model and support-model
experiments. The run includes the existing knowledge tests and the new capture
and support-boundary tests. It is not 237 independent capture scenarios.

## Implemented boundary

The [capture package](../experiments/knowledge/capture/README.md) has a compiled
[CSIL contract](../experiments/knowledge/schema/capture.csil), generated Go and
Rust codecs, a streaming matcher, current-input preparation, and a private
local spool. It does not expose a service endpoint.

Bindings identify the secret authority, reference path, key, session, issuance,
and binding ID. Values remain in active memory only. The archive uses four
typed segment variants: literal, secret, ambiguous, and gap. Display retains
Reactorcide's `${secret:path:key}` notation. Literal text in that notation is
not a request to resolve a secret.

The matcher retains possible prefixes across chunks. It handles short values,
equal values, contained and crossing overlaps, and rotation. Field provenance
can select the correct binding when values are equal. Free text preserves the
ambiguity. A final possible credential prefix becomes a gap rather than raw text.
This rule can suppress ordinary text, so the gap does not assert a secret existed.

Raw matching is always active for registered values. Standard base64, JSON-string
escaping, and URL-query escaping are optional registered transformations.
Unknown values and other transformations remain outside the guarantee.
The prototype is not a general secret detector or an opaque-file safety check.

`PrepareCurrent` calls an explicit authorized resolver. It rejects gaps and
ambiguity, obtains current values with new binding IDs, and registers them in
the new capture stream before return. Repeated uses of one old binding resolve
once during preparation. No tool or model executes. Historical values are not
requested, stored, or recovered.

## Recovery checks

Each spool batch belongs to an attempt, stream, and sequence. Publication uses
a synchronized private temporary file, an exclusive hard link, and directory
synchronization. A receipt contains the substituted payload digest. Different
content at an accepted sequence causes a conflict.

Tests kill only their own child process at three points:

| Process stop | Required recovery |
| --- | --- |
| After file synchronization, before publication | Pending file is not accepted; a retry can publish the batch. |
| After publication, before directory synchronization | The process-restart test can recover the published batch and its receipt. |
| After directory synchronization, before response | A retry recovers the same accepted content and receipt. |

Sixteen concurrent identical submissions recover one logical batch and one
receipt digest. Corrupt files, unsafe file types, changed sequence content,
missing ranges, and inconsistent complete markers are rejected or reported.
Retained pending files contain substituted evidence, not registered values.

These are process-crash checks on this Linux host. They do not prove survival
of power loss, storage-controller failure, a distributed filesystem failure,
or host loss. The directory has one trusted owner. A hostile local filesystem
writer is outside this experiment.

## Validation evidence

The full run checks both generated schemas, all four capture variants across
Go and Rust, the race detector, storage recovery, and current access rules.
`go vet ./...` also passes.

The [retained fuzz run](evidence/experiments.md#experiment-025)
passed 186,487 executions in a 20-second run with two workers. It compares
whole-input capture with different chunk partitions for fixed synthetic
registered patterns. This is not a count of distinct credential formats or
proof that unknown secrets cannot escape.

The original 200,000-segment keyword baseline still finds 50 of 100 expected
sources. Capture changes do not alter that retrieval result.

## Remaining integration work

The spool trusts an admitted capture component to supply substituted batches.
It cannot make a forged literal segment safe. Bind upload authority to stable
persona identity and admitted internal agent, attempt, and stream records.
Do not introduce temporary LinkKeys principals for these internal IDs.

Before real data, add authenticated upload, durable remote acknowledgements,
quota handling, recovery on another host, and current resolver authorization.
Repeat capture tests through actual model and executor adapters. Keep diagnostic
paths and failure logs inside the same substitution boundary.

Signed original bytes need separate handling. Verify before substitution;
a changed payload is a derived record, not independently verifiable original
authorization evidence. The current prototype does not implement this path.

## Next Actions

Complete W02 authentication and durable exact-byte approval. Connect the typed
capture path to a bounded executor and evidence uploader. Test upload after
the execution lease ends. Keep cleanup and authorized archive deletion separate.
