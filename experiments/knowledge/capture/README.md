# Reference only capture experiment

This package substitutes registered credentials before local archive storage.
It retains source bindings, not historical credential values. Its private
spool tests durable publication and exact retries on the local Linux filesystem.

The package is not a public upload service or a general secret detector.
Use synthetic credentials until authentication, admission, policy, and real
executor integration are complete.

## Data and capture

The source contract is `../schema/capture.csil`. Pinned generators produce
Go and Rust codecs in `../generated-capture/`. The four segment variants are
literal bytes, one secret binding, ambiguous bindings, and a capture gap.
Variant order is part of the experimental wire contract.

A binding records secret authority, binding ID, path, key, session, and optional
issuance ID. Display uses `${secret:path:key}`. The display string omits some
metadata, so do not use it as the identity of a binding.

Register each value before the tool or model receives it. `Stream.Push` holds
possible secret prefixes between chunks. One-byte values are not ignored.
The stream retains old registrations during rotation. Use a new binding ID
for each new issuance.

Raw bytes are always matched. Callers can also register exact standard base64,
JSON-string escaping, and URL-query escaping. Other encodings and arbitrary
transformations are outside this contract. Unregistered credentials can pass
through as literal data. A scan does not prove that a file has no secrets.

Matching rules are deterministic:

1. Use the longest complete binding when shorter matches are inside it.
2. Preserve equal-value ambiguity instead of selecting a binding.
3. Cover the full range of crossing overlaps. Preserve the ambiguity.
4. Use known field provenance through `Field` when it identifies the binding.
5. Record a gap if a final suffix could be an incomplete credential.

The last rule can discard ordinary text. It is conservative, not a claim that
the discarded text was a secret. Original byte offsets identify each range.
Offsets do not make substituted evidence an exact copy of original bytes.

The stream accepts chunks of at most 32 KiB and retains at most 64 KiB while
it resolves overlapping prefixes. A limit failure emits a gap and closes
the stream. It does not return the discarded raw bytes. The caller must retain
the gap and stop work that requires complete capture.

## Playback and new input

`Display` never calls a resolver. Literal reference syntax remains literal.
`PrepareCurrent` accepts typed secret segments and an explicit resolver.
The resolver must apply current authorization and return a new binding.
The function registers current values in the new capture stream before return.
It resolves a repeated old binding once per preparation.

Ambiguous segments and gaps stop preparation before any resolution. An expired
or missing value is an error, not a request to recover historical credentials.
Preparation does not run a command, call a model, or authorize a repeated effect.
Its returned bytes are for active use only. Do not log or archive them directly.

## Private spool

Create a private directory with mode `0700`, then call `OpenSpool`.
The caller supplies already-substituted typed batches. This is a trusted
capture-component boundary. A caller can still forge unsafe literal segments;
the spool does not replace authentication or the capture path.

Each sequence belongs to an attempt and a stream. `Put` writes a new private
temporary file, synchronizes it, publishes it with an exclusive hard link,
and synchronizes the directory before it returns a receipt. The receipt binds
the sequence to a digest of the substituted CBOR, not the original secret.

An exact retry returns the same receipt. Different bytes at the same sequence
cause a conflict. A crash before publication can leave a pending file; that
file is not an accepted record. Crashes after publication permit receipt
recovery. Pending files contain only substituted evidence.

`Inspect` checks a caller-declared last sequence, missing batches, final markers,
capture gaps, and byte-range continuity. It does not prove that all model or
process activity was captured. The local directory has one trusted owner.
Concurrent hostile filesystem writers, disk power loss, distributed filesystems,
disk quotas, remote upload, retention, and authenticated admission remain open.

## Test

From `experiments/knowledge/`:

```sh
node generate.mjs --capture --check
node run.mjs --corpus --count 3
node capture/fuzz.mjs
```

The full runner checks generated output and Go/Rust round trips. It also runs
the existing knowledge tests. Capture tests use temporary private directories
and fake credentials. Crash tests kill only their own child test process.
Test cleanup removes those temporary directories. Recorded reports remain.

## Next Actions

Connect capture to an admitted executor and an authenticated upload service.
Add durable upload acknowledgements, storage limits, and recovery on another host.
Keep environment deletion separate from evidence retention.
