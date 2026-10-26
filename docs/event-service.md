# Standalone event service

The event service accepts publications and retains delivery obligations in
PostgreSQL. A producer or consumer does not need Magi, a persona, or a task
session. LinkKeys verifies the calling application. The event service applies
its own access rules.

The executable is `cmd/events`. The combined Magi executable remains a separate,
read-only discovery service. This work does not change the Kubernetes deployment.

## Component contracts

Each service declaration has a file in [csil/components](../csil/components/).
The [aggregate schema](../csil/magi.csil) includes all components. The
[event schema](../csil/events.csil) includes only shared records, discovery,
and events. Generation produces separate Go and TypeScript event clients.
It also produces the combined clients. Existing wire IDs and stored record
vectors remain in the compatibility tests.

The event record retains an optional `session_id` for compatibility. The event
service does not require or interpret it. No command admission, actor binding,
workflow, or environment record is required.

| Operation | Signed body | Result |
| --- | --- | --- |
| Events/publish | Event | PublicationReceipt |
| Events/subscribe | EventSubscription | SubscriptionReceipt |
| Events/pull | PullEvents | DeliveryBatch |
| Events/acknowledge | AcknowledgeDelivery | DeliveryReceipt |
| Events/read-publication | ReadPublication | PublicationReceipt |

Discovery operations do not require authentication. Each event operation uses
`SignedRecord`. Its signature binds the exact input bytes, operation, component
schema, calling application, receiving application, key ID, and signature time.
The component schema is `catalyst.events`, version `0alpha1`. Use its descriptor
digest even when the client was generated from the aggregate schema.

The old `Events/read` and `Federation/deliver` declarations remain reserved.
The runtime does not implement them. Unsupported operations fail explicitly.

## Transaction boundaries

There are three different commitments:

1. Publication acceptance means that the event service stored the event and
   the delivery obligations for its current matching subscriptions.
2. Consumer acceptance means that the consumer stored the delivery and its
   acceptance receipt. The consumer then sends `acknowledge`.
3. Business completion means that the consumer completed its local work or
   reconciled an external result. Publish a separate result event when needed.

A receipt for one commitment does not prove another commitment.

Use `Local.EnqueueTx` in the producer's business transaction. It stores an
outbox entry in the producer's database. The service cannot make its own
publication transaction atomic with that remote business transaction.
An application can use the optional PostgreSQL helpers or implement the same
protocol with its own store.

`Local.PublishOne` claims one outbox row. It uses a database lease and an attempt
token. A stale publisher cannot complete a newer claim. A failed publication
is retried with backoff. After 20 attempts, the retained row requires operator
attention. `Local.Resume` permits an explicit retry. No retry rule deletes it.

The key for publication is the producer identity and event ID. The first
publisher application is also retained. The same publisher and event bytes
return the stored receipt. Changed bytes or another application instance
produce a conflict. Occurrence time does not limit the retry age. Each network
attempt needs a current authentication proof.

A publication retry also needs current publish permission. If that permission
was withdrawn after acceptance, the original authenticated publisher can use
`read-publication` to reconcile its stored receipt. That operation releases no
event payload and does not create another publication.

Use `Local.Accept` to commit a consumer inbox row before acknowledgement.
The inbox key contains the receiver, sending service, and delivery ID.
An identical delivery returns the saved receipt. Changed content conflicts.
`Local.AcceptTx` is available when acceptance must join another local transaction.
Do not send its returned receipt until that transaction commits.

`Local.ApplyTx` locks the accepted delivery. Commit its local business effect,
completion state, and any new outbox entry in the same transaction. The callback
must not call an external system. A duplicate does not run the callback again.

Before an external effect, call `Local.BeginExternal`. It commits
`outcome_unknown` before the call. A crash or lost reply cannot cause an automatic
repeat. Use the external service's idempotency or reconciliation API. Record the
result with `Local.ResolveExternal`. Permit another attempt only when evidence
shows that the prior effect did not occur. Database restore remains a separate
recovery problem; do not treat restart tests as restore tests.

## Subscription and policy rules

The initial policy uses exact application, producer identity, and event type
matches. Publish and receive grants are separate. Private source restrictions
must also match an allowed source policy revision. The application can publish
only under its own LinkKeys identity. A relay cannot claim another producer.
The service trusts an admitted producer to label its own data correctly.

A subscription belongs to one application instance. It selects one producer
and event type. It starts with future publications and is immutable. Retrying
the same subscription returns its original receipt. A changed filter under the
same ID conflicts. Subscription changes and backfill are not implemented.

Publication and subscription creation lock the same producer/type row. This
gives them a definite local commit order. There is no global event order.
An event's explicit recipient list further limits delivery.

Pull rechecks current receive permission and source restrictions. A withdrawn
grant cannot release a queued payload. The obligation remains stored. If the
grant returns, the consumer can receive it. A receipt acknowledgement by the
original consumer remains valid after read permission is withdrawn; it releases
no event content.

Use the policy command or `SetGrant` for policy changes. These operations take
an exclusive advisory lock. Admission and pull take the matching shared lock.
This prevents a policy change during the protected database transaction without
giving the serving role permission to modify policy rows.

Pull does not advance a client cursor. The service retains each unacknowledged
obligation. A consumer can repeat pull after restart. Parallel consumers can
receive duplicates and must deduplicate in their own shared store. Empty pull
means no currently permitted pending delivery, not a statement that no protected
event exists.

All local helpers assume that their caller already authenticated the peer and
authorized the operation. They are not public API handlers. Consumer-side
admission and outbound trust rules remain the consuming application's duties.

## Authentication and transport

The Go service uses the released LinkKeys SDKs. It resolves application keys
through its configured RP. It obtains domain keys and revocations separately
through pinned domain endpoints. The operator configures these endpoints and
pins. Caller input cannot supply a new network destination.

Key responses must match the complete application identity. Cache and
revocation checks must be current within five seconds. Unavailable, expired,
revoked, future, or invalid evidence fails closed. A request proof is valid for
one minute, with at most five seconds of future clock difference. These bounds
do not expire a retained event. An old event can use a new proof.

A temporary identity-service outage returns a retryable error. It does not
authorize the request and does not turn a temporary failure into a permanent
resource denial. The receiver checks request and key expiry again after lookup.

The operator must maintain domain pins when keys rotate. This initial service
does not provide automatic trust enrollment. It does not use a LinkKeys grant
as event authorization.

The HTTP carrier uses bounded CSIL-RPC over HTTPS. Loopback HTTP is permitted
for local development. Clients do not follow redirects. Configure the endpoint
and expected receiving application together. A public deployment requires a
trusted TLS endpoint. The receiver's configured identity is bound into each
request; a response receipt is not a separately signed federation artifact.

The service accepts no browser bearer session. Its writes need application
signatures. It rejects foreign browser origins and unexpected hosts. Liveness
does not query PostgreSQL. Readiness checks the store identity, schema version,
storage generation, and recovery status.

## Local setup

Run `npm ci --ignore-scripts`, `npm run generate`, and `npm run build:client`.
Go uses the pinned dependencies in `vendor`. Two vendored LinkKeys files have
a constructor compatibility patch for the current CSIL transport. The patch
adds constructor error handling only. It does not change signature verification.
Run `node scripts/vendor-linkkeys.mjs --check` to check it. Use the command
without `--check` to reproduce the vendor directory and patch. Do not run plain
`go mod vendor` and then assume the compatibility patch is still present.

Create a configuration file outside source control. It contains `application`,
`rp`, `domains`, and `grants`. It can also contain `storage_generation` and
`limits`. The [integration test](../tests/events-linkkeys.test.mjs)
constructs a complete example with synthetic identities. Use LinkKeys account
IDs, not display names. No signing private key is needed in this receiver.

Set `EVENTS_DATABASE_URL` to the component's PostgreSQL database. Use
`EVENTS_RP_API_KEY` for its RP credential. Do not store either value in a checked-in
configuration. Use an administrator database role for these setup commands:

```sh
go run ./cmd/events --config /path/to/events.json --migrate
go run ./cmd/events --config /path/to/events.json --apply-policy
```

The policy command updates only the listed exact grants. Omitted grants remain.
To withdraw a grant, include it with the relevant permission set to `false`.
Apply policy with a separate administrator role, not the serving role.

Use a restricted database role to start the service:

```sh
EVENTS_LISTEN=127.0.0.1:8081 go run ./cmd/events --config /path/to/events.json
```

The serving role needs database connection and schema use. Give it `SELECT`,
`INSERT`, and `UPDATE` on topics, subscriptions, publications, and deliveries.
Give it `SELECT` and `UPDATE` on `event_usage`. Give it only `SELECT` on
`event_grants`, `event_service`, `event_limits`, and `event_recovery`. It needs no DDL,
policy-write, delete, superuser, or Kubernetes permission. The service forces
`synchronous_commit=on`. The local transaction helpers require the same durable
commit setting in their own applications.

Set `EVENTS_ORIGIN` when a TLS proxy exposes another origin. Each independent
event service must have its own identity and database. Replicas of one service
share that service's database and identity. A store refuses a different identity.

The database schema is now version 2. Stop all version 1 server processes before
migration. Run `--migrate` with the administrator role, then update runtime grants.
The migration retains publications and computes the initial usage counters.
Old client requests remain compatible. An old server does not enforce the new
storage limits or recovery fence and must not run against the migrated store.

## Storage limits and inspection

The database holds one set of limits for all service replicas:

| Limit | Default |
| --- | --- |
| Retained publications | 100000 |
| Retained delivery obligations | 1000000 |
| Subscriptions | 4096 |
| Unacknowledged deliveries per consumer application | 10000 |
| Stored publication and delivery body bytes | 1073741824 |

Set all five fields in the optional `limits` configuration: `publications`,
`deliveries`, `subscriptions`, `pending_per_consumer`, and `payload_bytes`.
Each value must be positive. Run `--apply-limits` with the administrator role.
A lower limit does not delete existing records.

New admission reserves capacity in the same transaction as the publication and
its obligations. Concurrent replicas cannot exceed the configured limits.
Error 1429 is retryable. The producer keeps its outbox entry. Identical retries,
pull, and acknowledgements remain available when storage is full. An
acknowledgement releases pending capacity, but it does not delete retained data.

The byte counter covers publication and delivery bodies, not PostgreSQL files,
indexes, WAL, signatures, or receipt overhead. Separate record and wire bounds
limit those records. Monitor actual database and disk use. The shared capacity
reservation serializes admission briefly; throughput has not been measured.

Use `--inspect` with an operator database role for a bounded JSON view. It shows
usage, limits, recovery status, and up to 100 subscription rows. Each row has
pending and acknowledged counts and the oldest pending time. It does not return
payloads or credentials. Continue with `--after-consumer` and
`--after-subscription` from the last row. Use `--limit` to reduce the page size.
This is a database-authorized operator command, not a public queue API.

## Backup restore procedure

A database cannot detect a rollback of its data and its only recovery marker.
Keep `storage_generation` in deployment configuration outside the database
backup. Its initial default is `initial`. Use a new value for every restore.

1. Stop all replicas and remove their access to the old database. Stop dependent
   work that could act on restored records. Preserve the current stores when possible.
2. Restore into an isolated database. Change `storage_generation` in the external
   configuration. Do not restore that configuration from the same backup.
3. Run `--begin-recovery`. The new generation stays blocked. Readiness and all
   event data operations refuse work until reconciliation is complete.
4. Compare publisher receipts, consumer receipts, subscriptions, current grants,
   local inboxes, local effects, and external effects against their authorities.
   `Store.ComparePublication` and `Store.CompareAcknowledgement` provide
   read-only receipt comparisons while delivery is blocked.
5. Record the repair evidence and every accepted gap. Do not recreate a missing
   publication merely because its receipt is absent. New delivery IDs could
   repeat an external effect. A restored consumer store needs separate review.
6. Run `--complete-recovery --evidence EVIDENCE-REFERENCE` only after that review.
   Start replicas with the new generation and the current resource policies.

An exclusive recovery lock waits for admitted database transactions. Every data
operation holds the matching shared lock through commit. Old-generation replicas
remain fenced after recovery. The history retains the generation and evidence
reference. The command records the operator's decision; it does not repair lost
events, validate the evidence content, or prove that an external effect is absent.

Real `pg_dump` and `pg_restore` tests cover retained and missing publications,
lost acknowledgement state, unchanged consumer receipts, and stale replicas.
They also demonstrate an explicitly accepted synthetic gap. This is a tested
restore fence and comparison path, not automatic cross-store reconstruction.

## Current limits

An event is at most 16 KiB. It has at most 64 recipients and 32 source
restrictions. Pull returns at most eight deliveries. The publication path
supports at most 256 subscriptions per producer/type. Larger fanout requires
a bounded background expansion design. The carrier also enforces the shared
wire limits. These are safety bounds, not throughput claims.

No automatic retention deletion is enabled. There is no expired receipt that
could silently become a new effect. Operators must monitor storage capacity.
Automated cross-store repair, receipt pruning, subscription cancellation,
signed relays, push delivery, and public queue administration APIs need
further work before public deployment. This service is not a personal
notification inbox and does not implement quiet-hour rules.

The store retains the first authenticated publication proof and the consumer's
acknowledgement proof. Their signed input bytes remain unchanged. This is not
yet a complete policy-decision audit stream. The store does not scan for secrets.
Producers must replace secret values with references before durable publication.

## Verification

The final 2026-10-14 runs pass without failures or skips:

- The [main regression report](evidence/index.md#evidence-005)
  records 87 Go leaf checks and 382 Node checks per run, across three runs.
- The [event integration report](evidence/index.md#evidence-007)
  records 42 Go leaf checks and 23 Node checks per run, across three runs.
  The Node count includes the parent test and its 22 scenarios.
- Generation, TypeScript compilation, race detection, Go vet, source-drift
  checks, and local document links pass.

The event checks use producer and consumer SIGKILL, two HTTP replicas over one
event store, service SIGKILL, and PostgreSQL kill/restart. They also test current
permissions, stable receipts, retained request proofs, large response bounds,
real key revocation, and temporary identity failure. The two-domain test has
separate PostgreSQL containers and separate LinkKeys servers. Tests use a
restricted serving role that cannot change resource policy.
The new operational cases add real backup/restore, version 1 migration,
capacity contention, acknowledgement under capacity limits, and bounded inspection.

Run `npm test` for component generation, Go/TypeScript compatibility, strict
wire checks, HTTP checks, and the existing deployment scope tests.

Run `npm run test:events` for three recorded PostgreSQL recovery and real
LinkKeys integration passes. Reports include source hashes, dependency pins,
test counts, and tool versions. This command also checks the SDK compatibility
patch and compiles the pinned TypeScript SDK used by the test fixtures.

Run the PostgreSQL recovery tests with:

```sh
go test -race -tags=integration -count=1 -v ./eventing
```

Run the real LinkKeys boundary tests with:

```sh
node experiments/identity-service/prepare.mjs
node --test tests/events-linkkeys.test.mjs
```

The integration commands create only labelled, disposable Docker fixtures.
They use synthetic credentials. They remove their containers and volumes when
finished. The LinkKeys fixture needs the pinned sibling SDK checkout described
in the [authentication report](identity-service.md).

## Next Actions

Test backup reconciliation with the complete workflow and its effect stores.
Define storage alerts and review the event service's outbound network and TLS
configuration before public deployment. Connect the
[Corndogs journal](workflow-journal.md) without moving producer transactions
into the event service. Add push or relays when a use case needs them.
