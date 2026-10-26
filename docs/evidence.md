# Evidence and source map

Initial review date: 2026-09-27. Initial source observations were static.
On 2026-09-28 and 2026-09-29, real Corndogs and PostgreSQL containers were
tested locally. See [Corndogs 0.8.0 recovery results](corndogs-recovery-080.md)
and the linked historical report for the measured scope.
No persistent Magi deployment was made.

On 2026-09-30, local signed-evidence fixtures and three policy engines were
tested. See [identity and policy results](identity-policy.md). This used the
actual LinkKeys SDK, not a live LinkKeys login or network resolver.

On 2026-10-03, the execution and archive design was revised after local
Reactorcide source review. The current secret maskers retain values, not source
references. Both replace matches with a marker. The minimum match length is
three bytes in Go and three characters in Python.
Magi needs value-to-reference bindings, deterministic matching,
and stream-boundary handling. No Magi executor or archive test was run.

The 2026-10-05 user interview establishes the
[persona, session, and trust rules](personas-sessions-and-trust.md).
These decisions replace the proposed temporary-identity and historical-secret
requirements. They do not change the measured scope of earlier experiments.

The interview incorporated on 2026-10-06 establishes
[knowledge and interaction requirements](knowledge-and-context.md). It covers
multiple stores, scoped acceptance, live queries, concurrent voice and text,
multi-session replies, and extensible evidence layers. Recall times are user
targets, not benchmark results. The LinkKeys walkthrough is a required query
scenario, not new evidence of an implemented ActAs exchange.

On 2026-10-09, the user approved those requirements as a baseline that can change
with use. The [record and storage proposal](knowledge-storage.md) adds minimum
record and API shapes, a shared-reply example, and a storage comparison. Its
primary sources cover PostgreSQL search, recursive queries, row security and
timestamp precision; pgvector filtering; Qdrant multitenancy; Neo4j paths; and
SQLite WAL. The proposal links the sources beside the relevant claims.
Its workload and thresholds are proposed. No storage benchmark or new runtime
acceptance test was run for this design review.

On 2026-10-11, the user approved the proposal as a starting point. The
[knowledge experiment](knowledge-experiment.md) then passed 81 leaf tests across
three runs with synthetic data. This includes generated Go/Rust codecs,
storage access checks, concurrent correction ordering, and PostgreSQL recovery.
Full-text search found 50 of 100 sources per pass; all paraphrases were missed.
Two namespaces shared one disposable database. This was not live federation,
an authenticated service, or a full interaction-latency test.

On 2026-10-12, the [retrieval comparison](retrieval-comparison.md) used a local
Qwen planner and a pinned MiniLM ONNX model on CPU. Two separate test runs
compared six methods on 20 questions. Combined retrieval with explicit links
recovered every required source for the 18 questions with evidence. It still
returned candidates for both questions without evidence. These are retrieval
results, not answer-quality or production-scale measurements.

Further work on 2026-10-12 adds the [support report](evidence-support.md) and
[capture report](capture-experiment.md). The small local model matches the
expected support mode for 8 of 20 questions. Local typed capture and spool
recovery pass tests. The full regression records 237 leaf passes across three
runs. A retained chunk-partition fuzz run records 186,487 executions without
failure. No public API, production data, or persistent domain was used.

## Local ecosystem

Paths below assume sibling Catalyst Community checkouts. Shared notes live in
`~/agent-learning/`. They can contain historical deployment and
release details; verify those details before a change.

| Source | Observation and limit |
| --- | --- |
| [LinkKeys application keys](../../linkkeys/docs/application-keys.md) | Application key identity and attestations exist. This is not a general resource authorization engine. |
| [LinkKeys CSIL](../../linkkeys/csil/linkkeys.csil) | Canonical identities and application instances have defined fields. Verify persona and allowance integration without requiring temporary task identities. |
| [CSIL-Events](../../csilgen/docs/csil-events-transport.md) | A live typed-stream contract exists. Durable application receipts and cursor recovery are additional work. |
| [CSIL descriptors](../../csilgen/docs/csil-schema-descriptor.md) | Schema descriptors support inspection without converting exact CBOR values through JSON. |
| [Corndogs resilience contract](../../corndogs/docs/resilience.md) | Released 0.8.0 adds keyed submissions, guarded revisions, and retained receipts. Protected tests pass on five configurations. Legacy operations remain unsafe for these guarantees; receipt expiry limits retry safety. |
| [Corndogs storage](../../corndogs/docs/storage-backends.md) | Durability modes differ. Task durability and audit-log durability must be assessed separately. |
| [Reactorcide design](../../reactorcide/DESIGN.md) | Job execution, child workflows, secrets, and worker selection are present. Selected components are candidates for reuse; its coordinator is not the Magi execution contract. |
| [Go secret masker](../../reactorcide/coordinator_api/internal/secrets/masker.go) and [Python secret masker](../../reactorcide/runnerlib/src/secrets.py) | Value-only registration and marker substitution exist. They do not preserve reference provenance or streaming match state. |
| [Reactorcide secrets](../../reactorcide/docs/secrets.md) | Source for `${secret:path:key}` notation. Retain this display syntax with separate authority, version, and substitution metadata. |
| [Reactorcide worker interfaces](../../reactorcide/coordinator_api/internal/worker/interfaces.go) | One-command lifecycle and telemetry are useful inputs. A general command-session, terminal, and file API needs separate design. |
| [Reactorcide event publisher](../../reactorcide/coordinator_api/internal/pubsub/notify.go) | Live notification publication is not a durable transactional event feed. |
| [Reactorcide VM images](../../reactorcide/docs/vm-images.md) | Windows and macOS workers share image retrieval/cache operations. |
| [Longhouse](../../longhouse/README.md) | Task and identity integration exist. Role snapshots in access tokens affect prompt permission changes. |
| [Tinku receiver](../../tinku/api/internal/csilservices/federation.go) | Signed federation exists. Its one-hour freshness rule and duplicate refusal need different semantics for Magi delivery. |
| [Ichoi player contract](../../ichoi/server/schema/player.csil) | Player control exists. Conditional interruption/resume behavior requires an adapter review. |
| [TallyOwl](../../tallyowl/README.md) | Operational telemetry and correlation exist. Verify current transport/deployment state before reuse. |
| [Foundry](../../foundry/README.md) | Infrastructure management can support the test stack. |
| Private speech experiment | Local STT/TTS and browser streaming exist. The original source location is in the private operator notes. |
| Private speech streaming implementation | Streaming depended directly on the native Whisper implementation at review time. |

Shared notes checked at the start include Corndogs client behavior, csilgen
usage, LinkKeys SDK/application keys, Reactorcide workflows, CSIL TLS, and the
example deployment records. The 2026-09-27 Corndogs note now identifies
0.7.5 as the release for previously pending client and TLS changes. It says
to upgrade the server before clients that omit optional update fields.
The detailed execution review is in
`~/agent-learning/reactorcide-magi-execution-review.md` (private operator notes).
It records the inspected commit, spool and lease limits, VM credential
differences, and the scope of earlier package tests. These observations are
not Magi implementation or deployment evidence.

## External references

External projects are candidates or interoperability references. Their presence
here does not select them as an Magi dependency.

- [jr2 source review](jr2-review.md): inspected commit `fc67cff40d44fbceebc25c57d56c40ceb1111f28`. Reference observations only. No code reuse, dependency, or architecture change is intended. No project tests were run.
- [Open Workflow specification](https://github.com/open-workflow-specification/specification/blob/main/dsl-reference.md): reference for the workflow subset and versioned extensions.
- [Cedar policy semantics](https://docs.cedarpolicy.com/policies/syntax-policy.html): explicit permit/forbid evaluation.
- [CEL](https://cel.dev/) and [Go implementation](https://github.com/google/cel-go): candidate expression language and evaluator.
- [Cerbos conditions](https://docs.cerbos.dev/cerbos/latest/policies/conditions.html): CEL conditions in an authorization engine.
- [CloudEvents](https://github.com/cloudevents/spec/blob/main/cloudevents/spec.md): event metadata interoperability reference, not authentication.
- [NATS JetStream](https://docs.nats.io/concepts/jetstream): candidate broker.
- [Jepsen NATS 2.12.1 analysis](https://jepsen.io/analyses/nats-2.12.1): historical failure evidence. Test the selected release and durability configuration; do not generalize this result to every later release.
- [FCM sending](https://firebase.google.com/docs/cloud-messaging/send/v1-api) and [token management](https://firebase.google.com/docs/cloud-messaging/manage-tokens): direct push adapter references.
- [APNs sending](https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns): direct Apple push adapter reference.
- [SNS mobile push](https://docs.aws.amazon.com/sns/latest/dg/sns-mobile-application-as-subscriber.html): managed push candidate with direct endpoints.
- [Twilio Notify status](https://www.twilio.com/docs/notify): verify the current replacement offering before provider selection.
- [Wyoming](https://github.com/OHF-Voice/wyoming): optional voice protocol adapter.
- [Parakeet](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3) and [Granite speech](https://huggingface.co/ibm-granite/granite-4.0-1b-speech): model evaluation candidates.
- [lo-agent](https://github.com/IMJONEZZ/lo-agent): reference for inference capability discovery and controls.
- [Dev Containers](https://github.com/devcontainers/spec), [Webi](https://webinstall.dev/), and [mise](https://mise.jdx.dev/): environment and tool provisioning references.
- [Daytona documentation](https://www.daytona.io/docs/en/): API reference for environment lifecycle, processes, terminals, and files. The user selected it as inspiration, not as a runtime dependency.
- [CalDAV](https://www.rfc-editor.org/rfc/rfc4791): compatibility reference for a future calendar adapter.
- [OpenTelemetry span links](https://opentelemetry.io/docs/concepts/signals/traces/#span-links): operational mapping for multiple causal relationships.

## Repository state

The Magi directory was empty except for environment-owned metadata directories
when this work began. `git status` reported that it was not a Git repository.
No Git initialization, staging, commit, or push was performed. The design files
are ordinary workspace files and can be reviewed before repository setup.

## Next Actions

Record exact dependency revisions in each implementation experiment. Replace
static assumptions with test evidence before claiming a recovery guarantee.
