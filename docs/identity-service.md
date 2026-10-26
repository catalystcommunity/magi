# Authenticated allowance approval

Environment names and addresses in this document are examples. Original
deployment records are in the private operator archive. Select actual targets
through the [configuration overlay](../config/README.md).

Date: 2026-10-13 UTC. Status: W02's disposable integration gate is complete.

Magi now tests real LinkKeys login, durable approval of exact allowance bytes,
and approval verification across two independent domains. LinkKeys authenticates
the participants. The receiving service applies its own resource policy.
No persistent test domain or production service was changed.

The [implementation](../experiments/identity-service/approval.mjs) is an
experiment, not a deployed Magi core. It uses Node.js and SQLite to test the
service boundary. This does not select the production language or database.
The earlier [three-engine comparison](identity-policy.md) remains separate.

## Authentication and approval

The test creates two LinkKeys 0.16.2 servers. Each has its own domain keys,
accounts, database, application keys, and trust records. Private DNS supplies
the test endpoints. Both directions use the regular-RP SDK, encrypted login
callbacks, pinned backend TLS, and real remote key resolution.

1. Magi creates a random browser binding and stores pending login state.
2. LinkKeys authenticates the human and returns a signed identity assertion.
3. The SDK consumes pending state once. It verifies the browser binding,
   identity domain, audience, nonce, assertion lifetime, and user information.
4. Magi creates a short application session. Stored session and CSRF tokens
   are hashes. The session identifies the canonical account, not its handle.
5. Magi returns the exact typed allowance for review. The review binds its
   bytes and digest to that session.
6. Approval requires the session, matching origin, CSRF token, review ID,
   exact digest, and command key. The issuer must be the authenticated human.
7. One database transaction stores the immutable approval and its publication
   record. Only then can the service sign and publish the evidence.

The approval includes the original bytes, human identity, service identity,
approval time, login assertion time, and authentication evidence reference.
The application signature identifies the service that recorded approval.
It does not prove human attention or make that service trustworthy by itself.

The recorded authentication time is the assertion issue time. LinkKeys does
not include the last password or factor time in this assertion type. The test
limits the Magi session to five minutes. It does not claim to enforce a recent
password entry or a particular authentication factor.

## Retry and recovery rules

A retry with the same command key and exact bytes returns the original
approval. Different bytes under that key fail. A second command cannot create
another approval for the same human and allowance ID. New terms need a new
allowance ID and another review.

The publication worker stores its signed packet before delivery. A lost reply
causes delivery of the same packet. The receiver records one acceptance for
the approval service and approval ID. It rejects conflicting bytes.

SIGKILL tests cover both sides of the approval commit. Before commit, neither
record remains. After commit, both records remain and publication can continue.
Six independent processes that approve the same command return one approval ID.
These tests establish process recovery, not disk-loss recovery or database HA.

## Receiving service rules

The receiver must trust the approval service to identify humans from the
specified identity domains. It verifies the service key through LinkKeys.
It also checks the approved bytes, human, audience, signature context, and times.
Trust in an identity domain is not permission to approve another domain's data.

For each use, the receiver authenticates a new signed request from the delegate.
The resource, action, audience, and delegate must match the allowance. The
receiver applies current ownership and action policy after network checks.
It records the request nonce to reject replay.

The receiver requests current withdrawal status with a random challenge.
The approval service authenticates the status requester and checks its audience.
The signed reply binds the challenge, approval digest, audience, and check time.
The experiment requires a reply no more than five seconds old. Missing origins,
stale evidence, invalid signatures, and withdrawn allowances cause denial.

Accepted allowances remain separate from key lifetime. Tests show that an
allowance can remain valid after its signing key expires or is revoked. New
proofs and requests still need valid keys. The receiver can separately refuse
historical evidence from a key after a compromise finding.

A withdrawal check and a later business operation are not one distributed
transaction. The five-second status rule bounds evidence age; it does not
cancel an external operation already in progress. W04 and W05 must define
operation admission, effect receipts, and recovery.

## Protocol and evidence

The [CSIL schema](../experiments/identity-service/schema/approval.csil) defines
allowances, approvals, signed requests, status queries, and the prototype APIs.
Generated Go and TypeScript clients preserve the tested signed bytes.
Unknown fields and schema versions fail before approval. The original signed
bytes are retained; the service does not reconstruct them from display text.

This prototype accepts UTC timestamps with millisecond precision or less.
It rejects greater precision instead of rounding it. W03 must retain the
precision required by each final contract. The earlier policy experiment's
nanosecond and decimal checks are not replaced by this narrower profile.

The [recorded results](evidence/experiments.md#experiment-011)
include source hashes, SDK and image pins, per-run results, and Go vet output.
All three runs passed 34 leaf checks each: 102 passes, no failures, and no skips.
Go vet passed. The recorded source files did not change during the run.
The [runner](../experiments/identity-service/run.mjs) checks generated output
and detects changes to the recorded source files during a run.

## Limits before deployment

- The browser driver uses synthetic humans. It tests the API and cookie rules,
  not a rendered review screen, mobile browser, or human attention.
- The Magi test gateway binds to loopback. Production needs trusted HTTPS,
  explicit proxy rules, request limits, audit reason codes, and session cleanup.
- Each approval store uses one local SQLite database. Multi-process tests do
  not establish multi-host coordination or failover.
- Test DNS is private. The fixture permits private LinkKeys peers and accepts
  the isolated HTTPS server certificates. Backend RPC still uses key pins.
  No public DNS, certificate provisioning, or persistent peering was tested.
- Tests raise the TLS handshake limit and pace API-key requests. These are
  correctness tests, not throughput measurements or production capacity advice.
- Bootstrap credentials and application signing keys exist only in the test
  process or disposable containers. Production secret storage and renewal remain
  part of W09. No temporary task identities are required.
- The receiver tests access decisions. It does not update a real calendar.
  Policy editing, group authority, and general ActAs integration remain separate.
- The allowance API does not replace LinkKeys ActAs. It implements the user's
  selected Magi approval path. The broader relationship remains to be designed.

## Next Actions

1. Use this boundary in W03. Define the common service contracts and the
   production store interface without treating the experiment as a deployment.
2. Add W04 event publication and W05 effect receipts. Test lost replies and
   withdrawal races at the actual operation boundary.
3. Connect authentication to capture and knowledge APIs. Preserve each source's
   privacy and processing policy; authentication alone does not grant access.
4. Test rendered approval and operational recovery before the W07 deployment
   to catalystsquad.local and partner.local.
