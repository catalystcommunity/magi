CREATE TABLE IF NOT EXISTS event_service (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    application bytea NOT NULL, version integer NOT NULL CHECK(version=2)
);
ALTER TABLE event_service DROP CONSTRAINT IF EXISTS event_service_version_check;
ALTER TABLE event_service ADD CONSTRAINT event_service_version_check CHECK(version=2) NOT VALID;
UPDATE event_service SET version=2 WHERE version=1;
ALTER TABLE event_service VALIDATE CONSTRAINT event_service_version_check;
CREATE TABLE IF NOT EXISTS event_topics (producer text NOT NULL, kind text NOT NULL, PRIMARY KEY(producer,kind));
CREATE TABLE IF NOT EXISTS event_grants (
    application text NOT NULL, producer text NOT NULL, kind text NOT NULL,
    publish boolean NOT NULL, receive boolean NOT NULL, restrictions bytea[] NOT NULL,
    PRIMARY KEY(application,producer,kind)
);
CREATE TABLE IF NOT EXISTS event_subscriptions (
    consumer text NOT NULL, id text NOT NULL, producer text NOT NULL, kind text NOT NULL,
    body bytea NOT NULL, receipt bytea NOT NULL, PRIMARY KEY(consumer,id)
);
CREATE TABLE IF NOT EXISTS event_publications (
    producer text NOT NULL, id text NOT NULL, publisher text NOT NULL,
    body bytea NOT NULL, digest bytea NOT NULL, receipt bytea NOT NULL,
    proof bytea NOT NULL,
    PRIMARY KEY(producer,id)
);
CREATE TABLE IF NOT EXISTS event_deliveries (
    id text PRIMARY KEY, consumer text NOT NULL, subscription text NOT NULL,
    producer text NOT NULL, event_id text NOT NULL, kind text NOT NULL,
    restrictions bytea[] NOT NULL, body bytea NOT NULL, receipt bytea, acknowledgement_proof bytea,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY(consumer,subscription) REFERENCES event_subscriptions(consumer,id),
    FOREIGN KEY(producer,event_id) REFERENCES event_publications(producer,id),
    UNIQUE(consumer,subscription,producer,event_id)
);
CREATE INDEX IF NOT EXISTS event_pending ON event_deliveries(consumer,subscription,created_at,id) WHERE receipt IS NULL;
CREATE INDEX IF NOT EXISTS event_consumer_pending ON event_deliveries(consumer) WHERE receipt IS NULL;
CREATE TABLE IF NOT EXISTS event_limits (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    publications bigint NOT NULL CHECK(publications>0),
    deliveries bigint NOT NULL CHECK(deliveries>0),
    subscriptions bigint NOT NULL CHECK(subscriptions>0),
    pending_per_consumer bigint NOT NULL CHECK(pending_per_consumer>0),
    payload_bytes bigint NOT NULL CHECK(payload_bytes>0)
);
INSERT INTO event_limits VALUES(true,100000,1000000,4096,10000,1073741824) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS event_usage (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    publications bigint NOT NULL, deliveries bigint NOT NULL,
    subscriptions bigint NOT NULL, payload_bytes bigint NOT NULL
);
INSERT INTO event_usage SELECT true,
    (SELECT count(*) FROM event_publications), (SELECT count(*) FROM event_deliveries),
    (SELECT count(*) FROM event_subscriptions),
    (SELECT coalesce(sum(octet_length(body)),0) FROM event_publications) +
    (SELECT coalesce(sum(octet_length(body)),0) FROM event_deliveries)
ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS event_recovery (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    generation text NOT NULL, blocked boolean NOT NULL DEFAULT false,
    evidence text
);
CREATE TABLE IF NOT EXISTS event_recovery_history (
    generation text PRIMARY KEY, evidence text NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
