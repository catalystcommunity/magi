# Events and federation

Status: delivery requirements with an initial standalone PostgreSQL service.
The [implementation guide](event-service.md) defines the current pull API,
local transaction helpers, authentication, and remaining limits. NATS is not
required by this implementation. Push and relay delivery remain future work.

## Records

| Record | Meaning |
| --- | --- |
| Event | An immutable statement that a producer observed or committed something |
| Command | A request for a receiving service to perform an operation |
| Delivery | An obligation to send one event to one recipient |
| Attempt | One transport attempt for a delivery |
| Receipt | The recipient's recorded acceptance or rejection |
| Subscription | A recipient's authorized selection of events |

An event contains event ID, producer identity, schema reference, event kind,
resource reference, optional resource revision, occurrence time, recording time,
causal references, audience restrictions, and typed data or an artifact reference.
Identify a represented actor separately from the service that produced it.

Event IDs are unique within a producer authority. Delivery IDs are unique
within a sender authority. An attempt has a separate ID. A retry preserves the
event ID, delivery ID, and signed event bytes.

A delivery names a recipient service identity and, when needed, an intended
human identity. Sign the binding between the recipient, delivery ID, and event
digest. Authenticate a fresh attempt independently of the event's original
creation time. Do not accept an arbitrary old signed event for an arbitrary
new recipient.

## Commit and publish

For a service with a transactional store:

1. Authorize the command and validate the expected revision.
2. Commit the resource change, command result, and outbox row together.
3. Let a publisher claim the outbox row and send its event.
4. Keep retry state until the receiving service returns a durable receipt.

Publication can happen more than once. It must not disappear between a resource
commit and a process crash. A database notification can wake the publisher;
the outbox row remains the source of pending work.

For a service without this transaction boundary, an adapter needs a durable
operation journal and a reconciliation API. Document any interval in which an
effect can occur without a recoverable publication record. Do not promise the
same guarantee until that interval is resolved.

## Receive and acknowledge

1. Verify peer identity, recipient binding, signature context, and limits.
2. Check admission policy and subscription authority.
3. Atomically store the inbox record and durable acceptance receipt.
4. Acknowledge acceptance after that commit.
5. Apply business work through an idempotent command or local transaction.

A repeated delivery with the same digest returns the stored receipt. The same
ID with different bytes is rejected. Acceptance means responsibility for
processing, not successful completion. Send a separate result event when the
business operation completes.

One interaction can produce deliveries to several task sessions. Retain the
common source and interpretation reference with separate destination receipts.
Retry each admitted delivery without advancing another destination twice.
A routing correction is a new recorded decision, not a retry with silently
changed input. Reprocessing history does not itself authorize new deliveries.

If the receiver performs the effect in its own database, commit its deduplication
record, effect, and resulting outbox entry together. If it calls an external
system, use that system's idempotency or reconciliation API. A timeout after
an external effect can leave an unknown outcome. Do not retry that effect
blindly.

## Delivery state

```text
pending -> attempting -> accepted
                    -> retry_due -> attempting
                    -> rejected
pending/retry_due -> expired | cancelled
attempting -> reconcile_required
```

Retain a durable accepted obligation until its receipt or an explicit terminal
outcome. A retry limit moves it to operator attention; it does not silently
delete it. A dead-letter queue is a retained failure record, not successful
delivery. Event retention and delivery expiry are separate policies.

At-least-once delivery assumes the recipient eventually becomes available and
the obligation remains valid. Do not describe expired or rejected deliveries
as delivered. Store receipts long enough to cover permitted retry and restore
windows. If a receipt has been removed, require reconciliation rather than
assuming that an old request is new.

## Subscriptions and ordering

Filter on verified producer, represented actor where present, event kind,
resource, and destination. Bind filter evaluation to an authorized recipient.
Do not put private identity or resource names into publicly visible subjects.

Each durable subscriber has its own cursor and receipts. Push and pull are
delivery modes over the same retained records. Define a snapshot-plus-cursor
operation for a subscriber whose cursor predates retained history.

Use per-resource revisions to reject stale updates. A timestamp does not prove
order. Joins use causal references rather than a global clock. Group delivery
records the membership revision used to expand recipients. Recheck current
permission before releasing protected content to a queued recipient.

## Federation

Each side approves inbound and outbound cooperation independently. A peer can
permit event delivery without permitting execution, document replication, or
calendar access. Signatures establish attribution; they do not merge policies.

Record endpoint changes under the stable peer identity. Restrict outbound
connections to approved destinations; a peer must not use a supplied URL to
make Magi contact an unrelated internal service.

Protect payloads in transit. Decide whether each relay can read content.
Do not promise end-to-end confidentiality when a routing service needs plaintext.
Keep sensitive claims out of routine delivery headers.

NATS, if selected, is a private transport behind this protocol. Domains need
not share a NATS account, subject space, or operator. Select stream retention,
disk durability, replicas, consumer acknowledgement, and failure behavior in
the deployment profile. Broker deduplication does not replace business
idempotency. See [Evidence](evidence.md) for broker evaluation sources.

## Existing integration changes

- Magi must publish environment, control, and evidence changes durably.
- Reactorcide's live notification path needs durable publication when its CI/CD
  events are connected. It is not the required Magi execution path.
- Longhouse needs a durable task-completion feed or a recoverable change cursor.
- Tinku provides useful signing and peer-admission patterns. Its one-hour
  freshness window and duplicate-refusal response are not the Magi contract.
- Corndogs state changes need a recoverable bridge; see O05 in the decisions.

These are integration requirements. This documentation does not modify those
repositories or claim that these changes are already present.

## Next Actions

Use the standalone service's quotas, operator inspection, and restore fence.
Test reconciliation with workflow and effect stores before public deployment.
Connect producer-local outboxes before connecting notifications.
