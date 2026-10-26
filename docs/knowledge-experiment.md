# Knowledge storage experiment results

Measured 2026-10-11 in America/Denver. Result files use 2026-10-12 UTC.
The user approved the [storage proposal](knowledge-storage.md) as a starting
point. The first implementation tests its record links, admission rules,
privacy checks, recovery, and text search with synthetic data.

The repeated run passed 81 leaf tests: 27 tests repeated three times.
There were no failures, skips, or Go race-detector findings. `go vet ./...`
also passed. Full-text retrieval found 50 of 100 expected sources per pass.
It did not meet the proposed 95 percent recall target.

Retain PostgreSQL as the metadata and text-search baseline for the next
experiment. Compare semantic retrieval before selecting another search service.
These results do not complete W02, W03, W04, W10, W11, or W13.

The [2026-10-12 comparison](retrieval-comparison.md) adds local model and
embedding retrieval on a separate, smaller quality corpus. It does not replace
the results or the 200,000-segment workload below.

## Implementation and evidence

The [experiment directory](../experiments/knowledge/README.md) contains the
run instructions. The [CSIL schema](../experiments/knowledge/schema/knowledge.csil)
defines six record variants and minimum query, receipt, and error types.
Go and Rust packages come from the same pinned compiler and schema.
The [store](../experiments/knowledge/store.go) uses transactions for records,
receipts, routes, and pending publication. Corndogs task-state ownership is unchanged.

Use the [repeated-run summary](evidence/experiments.md#experiment-013)
for counts, source hashes, pins, and measurements. The
[raw test log](evidence/experiments.md#experiment-012)
contains each assertion result and the database version. The earlier result
files describe an earlier source revision. Do not combine their counts with
the repeated run. Parent test events are also counted in the summary; the
81 leaf events exclude those duplicate parent counts.

## Correctness results

| Area | Tested result |
| --- | --- |
| Shared reply | One original input supports two separate session inputs. Receipt and actual context use remain separate. Multiple uses can refer to one input. |
| Correction | A new interpretation preserves the original text and admitted content. It creates no delivery by itself. Stale revisions and duplicate routes fail. |
| Concurrent correction | Admission locks the selected interpretation until commit. A controlled lock test proves that a concurrent correction waits. A later stale admission fails. |
| Input integrity | Altered destination, content, or source lists fail admission. |
| Retry | Twelve concurrent requests with one key return one receipt. A changed body or conflicting producer sequence fails. |
| Private source | A derived record cannot drop source restrictions. A group session must be explicitly restricted before private input is admitted. |
| Current access | New source and session restrictions override retained labels and stale search indexes. History omits denied descendants. |
| Purpose and processor | Read permission does not imply enrichment permission. An untrusted processor fails. A persona name alone grants no access. |
| Representation | A fixture allowance row is required for representation. Its removal blocks a later read. This is not a LinkKeys allowance test. |
| Removal barrier | A deleted source blocks retained derivatives and old submission retries. Physical erasure is not implemented. |
| Identity and time | Equal record IDs under different authorities remain distinct. Database seconds and nanoseconds preserve the exact observation time. |
| Publication | A delayed lower source sequence receives a later feed position after commit. Concurrent publishers create no duplicate feed rows. |
| Restart | An accepted record, exact receipt, and pending publication survive a forced PostgreSQL process stop. |
| Query failure | Invalid limits fail. Cancellation, unreadable evidence, and an unavailable store are not reported as an empty successful search. |

The codec tests preserve all six record variants across Go and Rust.
They also preserve nanosecond timestamps, optional field presence, and the
exact decimal `9007199254740993.12345`. Both generated descriptors match.
Tests reject malformed Go input and an unknown enum in both languages.
They preserve optional assessment extension bytes without assigning meaning
to those bytes.

## Retrieval measurements

Each run loads 200,000 text segments and 20,000 corpus sessions.
Two authority namespaces share one disposable PostgreSQL database.
The corpus includes private, household, and work scopes, plus denied records
that match the query words. It uses a deterministic formula, not private data.

There are ten distinct subjects, repeated over five periods. Each period has
one exact-word query and one paraphrase. This gives 100 labelled cases, not
100 distinct subjects. Ten workers issue the queries concurrently.
Each query receives the expected time window, from one second before the
source time to one second after it. Natural-language time interpretation and
wide-window ranking are not tested.

Each run has a pass after database restart and a warm pass.
The host OS cache is not cleared. The restart pass is not a fully cold-cache
measurement. Timing covers the Go store call and SQL operations. It excludes
public RPC, model inference, speech, and device presentation.

| Run | Cache condition | p50 ms | p95 ms | Maximum ms | Expected source in first ten |
| --- | --- | --- | --- | --- | --- |
| 1 | Database restart | 5.023 | 28.610 | 32.557 | 50 / 100 |
| 1 | Warm | 1.709 | 3.012 | 4.047 | 50 / 100 |
| 2 | Database restart | 4.461 | 52.885 | 54.090 | 50 / 100 |
| 2 | Warm | 1.684 | 2.837 | 3.199 | 50 / 100 |
| 3 | Database restart | 5.618 | 71.142 | 75.071 | 50 / 100 |
| 3 | Warm | 1.668 | 2.752 | 60.124 | 50 / 100 |

Every pass found all 50 exact-word sources and none of the 50 paraphrase
sources. All 600 queries completed without query errors. No tested result
contained a denied or foreign-namespace record. The combined recall was
300 / 600, or 50 percent. The proposed target was 95 percent.

Latency includes unsuccessful searches. These numbers do not establish the
three-second useful-recall target. The experiment generates no final answers,
so it does not measure answer correctness. Short text entries and supplied
time windows also limit the comparison with real conversation history.

## Test configuration

| Component | Measured configuration |
| --- | --- |
| Host | Linux x86-64, AMD Ryzen 7 5800X, 16 logical CPUs |
| Database | PostgreSQL 17.11; image digest in toolchain.json |
| Container limits | Two CPUs and 1 GiB memory |
| CSIL | csilgen 0.2.9; compiler and Go/Rust generator hashes verified |
| Go | go1.27.0-X:nodwarf5; pgx/v5 5.7.1; race detector enabled |
| Rust | rustc 1.98.0; locked Cargo build; chrono 0.4.45 |
| Runner | Node.js 26.1.0 |

The [pin file](../experiments/knowledge/toolchain.json), Go module files,
Cargo lockfile, and generated descriptors identify the tested dependencies.
No model, embedding service, or delayed live source took part.
The test did not measure a payload-size distribution or a memory-use profile.

The first restart attempt exposed a harness error. Docker changed an
automatically allocated host port after restart. The test still used the old
port. The harness now selects an unused loopback port and fixes it for the
container lifetime. The repeated recovery checks passed with that change.

The harness removed each synthetic database container and its volume after
the tests. Source fixtures and result logs remain. No persistent domain,
existing database, or production credential was changed.

## Remaining integration limits

Fixture code supplies identities, processor permissions, and representation
grants. There is no authenticated API listener or durable human-approval
service. Complete W02 before exposing these operations. The experimental
CSIL service IDs and error types need review before production use.

The runtime role is not a database owner or superuser. However, it has table
access, and SQL row security policies are absent. Application checks enforce
the fixture policy. Concurrent revocation of general grants is not proved by
the interpretation-lock test. It needs a separate service policy contract.

Two namespaces in one database do not prove federation. Mirror recovery,
independent keys, foreign withdrawal, and separate-domain failures remain open.
The local publication feed has no network receiver. The delivery table records
fixture receipts and context use, not actual agents or external tool effects.

Original text in this first experiment is synthetic and already in archive
reference form. The later [capture prototype](capture-experiment.md) adds typed
substitution and local spool recovery. Payload upload recovery, media storage,
and deletion propagation remain open. Authorship acceptance, live calendar queries, adaptive interaction,
and later reflection are also outside this first implementation.

History traversal is bounded to 128 records. The schema reserves opaque cursor
fields, but the store does not implement pagination. The SQL receipt and use
tables retain the tested facts, not all fields of the proposed archive protocol.

## Next Actions

1. Extend the tested retrieval paths from the separate comparison to a scale
   workload. Preserve this original baseline and its visible misses.
2. Expand the corpus with distinct questions, broad periods, long histories,
   and cross-session context. Measure successful recall and answer support.
3. Complete W02 and review the experimental CSIL types at the service boundary.
   Add authenticated pagination and policy-change tests before an API release.
4. Integrate the tested secret substitution and local spool before real capture.
   Add authenticated upload and recovery on another host.
   Keep physical deletion and cross-domain recovery as explicit test gates.
