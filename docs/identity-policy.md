# Identity and policy experiment

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

Date: 2026-09-30. Scope: the original local W02 policy comparison.
The [2026-10-13 service experiment](identity-service.md) completes the disposable
authentication, durable approval, and cross-domain integration gate.

The local experiment passed 495 leaf checks across three runs. It uses the
LinkKeys Go SDK, real Ed25519 signatures, and three actual policy engines.
It does not use a live LinkKeys server or a durable approval store.

The user selected authenticated approval of exact allowance bytes, recorded
by Magi. The application signature identifies the application. It does not
replace the human approval record. Each receiving service still decides
whether the issuer can authorize access to the requested resource.

Recommendation: use embedded CEL for the first Go policy prototype. Keep the
verification and policy interfaces separate. This recommendation does not
select Magi's production language or mandate CEL in other domains.

## Tested boundaries

| Check | Result and scope |
| --- | --- |
| T01 valid signature without resource authority | Denied by all three engines in both domain directions |
| T23 different recipient | Rejected before policy evaluation |
| T24 invalid or stale key evidence | Rejected for tested expiry, revocation, signature, identity, and freshness cases |
| Guest music access | Kitchen access permitted; another room denied |
| Calendar allowance | Proposal permitted; deletion denied |
| Third-party attribute | Trusted signer accepted; another valid signer or wrong subject denied |
| Indefinite allowance | Accepted without an allowance expiry; withdrawal stops access |
| Policy disagreement | Explicit deny overrides permit |
| Human approval | Exact bytes bound to the authenticated canonical identity |
| Changed allowance | Changed scope, delegate, recipient, or expiry needs new approval |
| Retained approval | Accepted allowance survives issuer-key expiry; each new request needs current authentication |
| Exact value conversion | Large decimal differences and one-nanosecond boundaries retain their meaning |

The fixtures use `primary.opal.test` and `secondary.opal.test`. Each has an
independent synthetic domain key. The tests reverse sender and receiver roles.
Neither catalystsquad.local nor partner.local was changed.

## Verification and authorization

The receiving service performs these steps:

1. Obtain keys from an authenticated resolver. Do not trust keys supplied
   with an untrusted request as the trust anchor.
2. Check local and upstream freshness, fetch age, and revocation-check age.
3. Use the LinkKeys SDK to verify the application attestations and revocations.
4. Check the full account, domain, application, and instance binding.
5. Check current key use, statement signature, schema, context, and recipient.
6. Convert verified values without loss. Obtain ownership and withdrawal
   state from receiver-owned records.
7. Evaluate local policy. Deny on an error, unknown result, or non-Boolean result.

The experiment uses a one-minute evidence age limit and no clock-skew margin.
These are test settings, not selected deployment defaults. Domain keys are
fixture trust anchors. DNS discovery, TLS pinning, and domain-key revocation
retrieval are not part of this run.

The SDK can report a locally fresh result whose upstream RP status is stale.
Both levels must satisfy the receiver's freshness rule. The experiment also
checks key creation and attestation issue times before current use. The
inspected SDK's `ClassifyKey` checks expiry and revocation, not these future
start times.

Canonical account identity is separate from application identity. The fixture
resource policy selects one owner application instance. This is a local rule,
not a claim that every application key of an account has resource authority.
Handles do not participate in identity equality.

## Human approval contract

The selected path is an authenticated approval operation in Magi.

1. Prepare a typed allowance with its issuer, delegate, scope, recipient,
   validity, schema, and withdrawal reference.
2. Present the exact meaning to the human through an API client. The backend
   must not trust identity or approval flags supplied by that client.
3. Validate the authenticated session and its canonical human identity.
   Apply the operation's session-age, CSRF, and reauthentication rules.
4. Commit an immutable approval record for the exact allowance digest.
   Bind the approving identity, time, and authentication evidence reference.
5. Sign or publish the approved statement only with its approval reference.
   Recover publication from the committed record after a crash.
6. Require new approval if the statement bytes change. Retrying the same
   approval must return the original record.

The prototype models the binding after authentication. Its `recordApproval`
function does not perform login, session validation, or a database commit.
It verifies a signed fixture statement before recording a simulated approval.
It rejects invalid new statements and approvals from a different account or
domain. Stored records are in memory and are not safe for concurrent service use.

A foreign receiver needs authenticated approval evidence from a service it
trusts for that attribution. A caller-supplied Boolean or unsigned approval
reference is insufficient. The receiver decides which approval services it
trusts and whether the named human has resource authority. LinkKeys neither
records the resource permission nor makes this decision.

Retain the verified allowance and its approval after admission. Do not require
the original signing key to remain current forever. Authenticate new requests
with current keys. Continue to apply allowance expiry, withdrawal, and local
policy. Key revocation and allowance withdrawal are separate events.
The test covers issuer-key expiry; compromise response and historical evidence
verification still need explicit rules.

## Policy engine comparison

| Candidate | Measured behavior | Required integration |
| --- | --- | --- |
| Embedded CEL 0.30.0 | All shared cases pass. Invalid policies fail compilation. Missing and invalid deny facts cause denial in the wrapper. | Magi supplies policy composition, typed inputs, cost bounds, versioning, and decision records. |
| Cedar 4.13.0 | All shared cases pass. Schema validation rejects an unknown field. Native evaluation can allow when a forbid rule has an error. | Inspect diagnostics and deny on evaluation errors. Use the library API in production, not CLI output parsing. |
| Cerbos 0.55.0 | All shared cases pass with strict evaluation. The default configuration allows the missing-deny-fact probe. | Set `engine.strictEvaluation=true`. Operate a policy process and protect its input API. |

The unsafe-default checks pass when they reproduce the documented behavior.
They do not approve that behavior for Magi. Cedar documents skip-on-error;
Cerbos provides strict evaluation to deny affected actions.
[Cedar authorization](https://docs.cedarpolicy.com/auth/authorization.html),
[Cerbos strict evaluation](https://docs.cerbos.dev/cerbos/latest/configuration/engine.html#strict-evaluation).

CEL fits the first prototype because it embeds in the existing Go experiment
and matches the preferred expression syntax. It does not supply a complete
authorization service. A permit expression alone is insufficient. The wrapper
evaluates both permit and deny policies, checks Boolean results, and applies
a cost limit. No production engine choice is final.

Cerbos offers a separate policy service with its own configuration and API.
Cedar offers a dedicated policy language and schema validator. Neither needs
to implement LinkKeys authentication itself: a trusted service boundary can
verify evidence before it calls the engine. A fork is not required for this
tested integration boundary.

## Exact values

The shared fixtures compare decimal strings with exact rational arithmetic
and timestamps with Go nanosecond precision. They pass verified comparison
results to all three engines. No raw decimal is converted to a float.

This proves the narrow conversion path, not native CSIL decimal support in
every policy engine. Cedar's native probes accept four decimal places but
reject five. They accept milliseconds but reject nanosecond timestamp text.
These limits agree with its documented types.
[Cedar data types](https://docs.cedarpolicy.com/policies/syntax-datatypes.html).

W03 needs typed CEL bindings or bounded comparison functions for policies
that compare arbitrary CSIL decimals. It also needs shared timestamp and
identity conversion fixtures. Do not reduce precision to fit an engine.
Reject unsupported values or preserve them through an explicit adapter.

## Measurements

Host: Linux amd64, AMD Ryzen 7 5800X. Go 1.27.0. These are small local samples,
not capacity estimates. Timing runs did not use the race detector.

| Measurement | Observed value | Limit |
| --- | --- | --- |
| Warm CEL decision | 2.17 to 2.25 microseconds; about 1075 bytes allocated | Two compiled expressions; excludes signature verification |
| Current request verification and CEL decision | 208 to 210 microseconds | Retained allowance; synthetic resolver evidence; no network |
| Warm CEL compilation | 265 to 271 microseconds; about 163 KB allocated | Same small policy; not a cold process start |
| First CEL compilation in a separate test run | 1.42 milliseconds; process RSS 21684 KiB | RSS includes the Go test process and SDK |
| Cedar reported authorization time | Median 18 microseconds over 120 cases | Fresh CLI process for each case |
| Cedar full process call | Median 3.33 milliseconds | Includes process start, parsing, and output |
| Cerbos loopback request | Median 539 microseconds over 120 cases | Includes HTTP encoding, transport, and decoding |
| Cerbos readiness | About 62 milliseconds across three starts | Poll interval is 20 milliseconds |
| Cerbos process RSS | 86312 to 92828 KiB | Sampled at readiness and after the fixture set |

A separate minimal Cedar error probe used 11496 KiB peak RSS. It is not the
full shared policy. Do not compare it directly with the Go test process or
Cerbos service. Cedar RSS values logged by the Go test runner can include
process-creation overhead; they are not used as an engine memory comparison.
These different measurement paths do not establish a general speed ranking.

## Evidence and versions

- LinkKeys checkout: `a1ddbbf67fe542ee04476d1178f8618e2147d52d`.
  The regular-RP Go SDK had no local changes. Unrelated local-RP changes
  were preserved. The Go SDK has no matching module release tag locally;
  the experiment uses a sibling checkout replacement.
- CEL: `github.com/google/cel-go v0.30.0`, recorded in `go.mod` and `go.sum`.
- Cedar archive: `23d358ef38edad8ade629a09f6a7359ecbd1761c9ff82e9fd0a5c5bce36b8c79`.
- Cerbos archive: `01cb5ae0b888393219846c1bc43d4ed1a4701a51fb8ae38523d3a378e685efca`.
  Both archives matched the published release checksums before execution.

See the [experiment instructions](../experiments/identity-policy/README.md),
[final fixture results](evidence/experiments.md#experiment-008),
[non-race timing results](evidence/experiments.md#experiment-010),
and [benchmarks](evidence/experiments.md#experiment-007).
The final suite has 165 leaf checks per run, repeated three times. There are
no failures, skips, or race warnings. `go vet` passed. The LinkKeys Go SDK's
own [race-enabled tests](evidence/experiments.md#experiment-009)
also passed.

Each Cerbos process used loopback listeners, synthetic inputs, and disabled
telemetry. Tests stopped their own processes and removed temporary policy
files. No LinkKeys source, production data, or deployment was changed.
No Git staging, commit, or push was performed.

## Next Actions

1. Use the tested service boundary and CSIL records in W03. Preserve exact
   decimal and timestamp handling when adding policy bindings.
2. Add production store, trust-policy editing, and deployment controls. The
   service experiment does not select a production stack.
3. Connect approval publication and operation receipts to W04 and W05.
