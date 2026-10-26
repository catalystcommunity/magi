# Native protocol and service baseline

Magi uses the `0alpha1` CSIL contract in [csil/magi.csil](../csil/magi.csil).
Go and TypeScript clients use the same descriptor and wire fixtures. This is
the first service contract outside the experiment directories. It is not a
claim that every declared service is implemented.

Only `Discovery/describe` and `Discovery/read-schema` are enabled. The service
does not accept identities, start tasks, resolve secrets, or store evidence.
Do not send private data or credentials to this discovery service.

## Contract groups

| File | Records |
| --- | --- |
| [Shared values](../csil/types/shared.csil) | Identities, resources, schemas, scope, restrictions, exact quantities, and signature input |
| [Identity](../csil/types/identity.csil) | Allowances, recorded approval, withdrawal, personas, trust, and evaluation |
| [Coordination](../csil/types/coordination.csil) | Sessions, worker admission links, commands, events, receipts, and Corndogs links |
| [Execution](../csil/types/execution.csil) | Hosts, native processes, containers, VMs, resource bindings, holds, and control |
| [Evidence](../csil/types/evidence.csil) | Secret references, streams, context, source records, interpretations, and actual inputs |
| [Services](../csil/services.csil) | Assigned service and operation IDs |

The baseline keeps joining, holding, pausing, and command cancellation separate.
Task links refer to Corndogs. They do not create another editable task state.
An internal worker ID does not create a LinkKeys principal.

Context access requires an actor and control scope. A persona and represented
human are optional. Each source restriction retains its owner and policy.
Consumers must satisfy every applicable restriction. A public default cannot
remove a private source requirement.

One interpretation can link several original inputs and destination sessions.
Each session input retains its own content and admission reference. Delivery
and context use are separate records. Later assessments have optional typed
dimensions. They do not change earlier inputs or grant execution rights.

## Wire profile

| Limit | Value |
| --- | --- |
| Complete frame | 256 KiB |
| One byte string, including an embedded payload | 128 KiB |
| One text value | 16 KiB |
| One array or map | 256 entries |
| Nesting depth | 24 |
| Values in one frame | 8192 |
| HTTP request deadline | 10 seconds |
| Concurrent requests per service instance | 32 |
| Returned service error text | 256 characters maximum |
| Ordinary integer magnitude | 9007199254740991 maximum |

The profile rejects duplicate keys, unknown record fields, trailing bytes,
indefinite values, non-minimal integers, unsupported tags, and invalid UTF-8.
It checks limits before it calls a generated decoder. Embedded payload bytes
need separate validation against their declared schema at the consumer.
Generated decoders alone do not enforce this boundary.

The initial Go/TypeScript profile accepts UTC timestamps with at most three
fractional digits. It rejects finer precision instead of rounding it. The
TypeScript generator uses `Date`, which cannot retain nanoseconds. Keep original
source bytes in a typed payload or artifact when more precision is necessary.
Do not convert that source time into a lower-precision authoritative fact.
A later profile can use a lossless timestamp adapter. The earlier Go/Rust
experiment remains the reference for nanosecond round trips.

Decimals use CSIL tag 4, not a floating-point string conversion. The profile
limits the decimal exponent to -128 through 128. Codec checks reject values
that cannot survive the selected target's exact round trip.
The ordinary integer limit does not apply to a decimal mantissa. Both clients
reject ordinary integers outside their common exact range.

## Signatures and approval

`SignedRecord.input` contains the exact encoded `SignatureInput` bytes. The
signature covers those bytes. This input binds the context, schema digest,
signer, recipient, key ID, time, and body. The receiver supplies the expected
context and participants independently. It retains the verified bytes.

Go can encode an instant as `.12Z` while TypeScript uses `.120Z`. Verification
must not reconstruct the input to sign. Tests sign in each language and verify
in the other. Changes to the body or any signed binding fail verification.

The signature helper is not an authentication or authorization service. Its
caller must resolve a trusted key, check current status where required, validate
the typed body, and enforce time and resource policy. Use W02's tested LinkKeys
boundary for this work. Human approval, allowance withdrawal, current request
authentication, and historical key compromise remain separate decisions.

The backend derives the actor from authentication. Actor fields in incoming
records are claims until the backend checks them. The schema does not enforce
privacy, ownership, or policy by itself.

## Secret reference boundary

Archive content uses literal, secret, ambiguous, or gap segments. Familiar
`${secret:path:key}` display text does not identify a segment type. Viewing
history never invokes a resolver.

The preparation helper accepts an independently authorized current resolver.
It requires a new binding for the destination session and checks its validity.
Missing credentials, ambiguity, gaps, and unsupported encodings cause refusal.
It supports raw substitution initially. It does not retrieve historical values.
Register the returned bindings with capture before using the transient values.

These helpers do not detect unknown secrets. The tested streaming capture
component still needs an adapter to the new records and authenticated upload.
Do not claim complete archive protection from a wire type alone.

## Compatibility and transport

CSILgen and both generator modules are pinned by SHA-256 in
[toolchain.json](../csil/toolchain.json). The Go transport is pinned to CSILgen
commit `baec1dad191c4ccd522772b5172ee12ba5bf3f6c` in [go.mod](../go.mod).
The TypeScript compiler is pinned to 5.9.3. The TypeScript HTTP adapter is
tested against the reference Go CSIL-RPC transport.

Use CSIL-RPC v1 envelope-in-body at `POST /csil/v1/rpc`. A typed application
error has transport status zero and variant `ServiceError`. Nonzero transport
status means that no typed result is available. Clients check correlation IDs,
variants, response limits, and deadlines. They do not follow redirects.

Discovery lists only enabled operations. A schema declaration is not a service
capability. Unsupported protocol versions, required features, and schema digests
are refused. Error code 1001 means invalid input. Code 1002 means unsupported
semantics. Neither error is automatically retryable.

Keep service IDs and positional union arms stable. The checked-in
[baseline vectors](../tests/baseline.json) test older bytes against current
code. Change the negotiated profile when required semantics change. Optional
assessment data belongs in versioned typed extensions; an unknown extension
must not become an executable instruction.

## Local development

Install Go 1.27 and Node.js 26. The pinned generator binary and modules are
needed only for generation and the generation check. Repository tooling never
changes the shared generator directory.
The generator script first uses `CSILGEN` and `CSILGEN_MODULES` if set. Otherwise,
it prefers `.build/csilgen/<version>/csilgen` and the `generators` subdirectory.
Each file must match [the recorded hashes](../csil/toolchain.json). If this local
cache is absent, the script checks the shared installation against those same
hashes. A changed shared installation is not permission to change the pin.
The current compiler checksum is for Linux amd64. Another generator host
needs its own verified binary checksum. Do not disable the checksum check to
run generation on another platform.

```sh
npm ci --ignore-scripts
npm run build:client
npm test
npm run dev
```

The service listens on `127.0.0.1:8080` by default. In another terminal:

```sh
node scripts/probe.mjs http://127.0.0.1:8080/csil/v1/rpc
go run ./cmd/magi-probe http://127.0.0.1:8080/csil/v1/rpc
```

Set `MAGI_LISTEN=127.0.0.1:0` to choose a free local port. The startup message
shows the origin. Non-loopback deployments require an explicit HTTPS
`MAGI_ORIGIN`. The listener itself uses HTTP behind the selected TLS gateway.

`npm test` checks generated output, compiles TypeScript, runs both test suites
three times, runs the Go race detector and vet, and checks documentation links.
It writes source hashes and results under `test-results/`. Socket and process
tests require a development environment that permits local listeners.

## Measured checks

The original W03 baseline has 110 type rules, including 94 records. It assigns
nine service IDs and sixteen operation IDs. Its deployed executable enables
only two discovery operations.

The W04 source adds seven records and five event operations. It preserves the
old vectors and IDs. Component declarations now have separate files. The
W04 aggregate schema contains 117 rules, 101 records, nine services, and 21 operations.
The event component can generate clients independently. Its separate executable
enables discovery and five protected operations. See the
[event service guide](event-service.md). The deployed read-only image still has
the original descriptor; current-source probes require a matching server build.

The independent workflow component adds 25 rules, including 22 records, and
eight protected operations. The current aggregate has 142 rules, 123 records,
ten services, and 29 operations. `csil/workflows.csil` generates a standalone
client with no task manager, persona, session, or event-service requirement.
See the [workflow service guide](workflow-service.md). The old wire IDs and
fixtures remain unchanged. The public deployment still has the W03 descriptor.

On 2026-10-13, each of three runs passed 52 Go leaf checks and 355 Node checks:
1,221 checks in total, with no failures or skips. The
[final local report](evidence/index.md#evidence-002)
records the source hashes. Generation checks, TypeScript
compilation, the Go race detector, and Go vet also passed. The result files in
`test-results/` retain the tested source hashes.

The checks cover record round trips with optional fields present and absent,
every union arm, fixed older vectors, unknown-field refusal, exact decimals,
signatures in both directions, secret-reference preparation, public-operation
limits, HTTP failures, and process shutdown. They do not prove policy or task
execution for the declared but disabled APIs.

## Next Actions

Keep the deployed service read-only until event operational recovery gates pass.
Apply semantic authorization and lifecycle checks at each new component.
Preserve the existing vectors when extending the aggregate schema.
