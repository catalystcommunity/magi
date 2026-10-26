-- Disposable experiment only. The harness owns this database.
CREATE TABLE scope_readers (
    subject text NOT NULL, scope text NOT NULL, can_write boolean NOT NULL,
    PRIMARY KEY (subject, scope)
);
CREATE TABLE processor_grants (
    processor text NOT NULL, scope text NOT NULL, purpose text NOT NULL,
    PRIMARY KEY (processor, scope, purpose)
);
CREATE TABLE representations (
    actor text NOT NULL, subject text NOT NULL, scope text NOT NULL, purpose text NOT NULL,
    PRIMARY KEY (actor, subject, scope, purpose)
);
CREATE TABLE sessions (
    authority text NOT NULL, id text NOT NULL, scopes text[] NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (authority, id)
);
CREATE TABLE records (
    authority text NOT NULL, id text NOT NULL, kind text NOT NULL,
    producer text NOT NULL, stream text NOT NULL, source_sequence bigint NOT NULL,
    payload bytea NOT NULL, scopes text[] NOT NULL, purposes text[] NOT NULL,
    session_id text, observed_seconds bigint NOT NULL, observed_nanos integer NOT NULL,
    deleted boolean NOT NULL DEFAULT false,
    enrichment_allowed boolean NOT NULL DEFAULT true,
    PRIMARY KEY (authority, id),
    UNIQUE (authority, producer, stream, source_sequence),
    CHECK (observed_nanos >= 0 AND observed_nanos < 1000000000)
);
CREATE TABLE dependencies (
    authority text NOT NULL, id text NOT NULL,
    source_authority text NOT NULL, source_id text NOT NULL,
    PRIMARY KEY (authority, id, source_authority, source_id),
    FOREIGN KEY (authority, id) REFERENCES records(authority, id),
    FOREIGN KEY (source_authority, source_id) REFERENCES records(authority, id)
);
CREATE INDEX dependencies_reverse ON dependencies(source_authority, source_id);
CREATE TABLE command_receipts (
    authority text NOT NULL, actor text NOT NULL, operation text NOT NULL,
    command_key text NOT NULL, digest bytea NOT NULL, record_id text NOT NULL,
    receipt bytea NOT NULL,
    PRIMARY KEY (authority, actor, operation, command_key)
);
CREATE TABLE interpretation_heads (
    authority text NOT NULL, root_id text NOT NULL, current_id text NOT NULL,
    revision bigint NOT NULL,
    PRIMARY KEY (authority, root_id), UNIQUE (authority, current_id)
);
CREATE TABLE outbox (
    authority text NOT NULL, id text NOT NULL, published boolean NOT NULL DEFAULT false,
    PRIMARY KEY (authority, id),
    FOREIGN KEY (authority, id) REFERENCES records(authority, id)
);
CREATE TABLE feed_counters (authority text PRIMARY KEY, last_position bigint NOT NULL DEFAULT 0);
CREATE TABLE feed (
    authority text NOT NULL, position bigint NOT NULL, id text NOT NULL,
    PRIMARY KEY (authority, position), UNIQUE (authority, id),
    FOREIGN KEY (authority, id) REFERENCES records(authority, id)
);
CREATE TABLE deliveries (
    authority text NOT NULL, id text NOT NULL, session_id text NOT NULL,
    received boolean NOT NULL DEFAULT false,
    PRIMARY KEY (authority, id),
    FOREIGN KEY (authority, id) REFERENCES records(authority, id)
);
CREATE TABLE context_uses (
    authority text NOT NULL, id text NOT NULL, context_id text NOT NULL,
    PRIMARY KEY (authority, id, context_id),
    FOREIGN KEY (authority, id) REFERENCES deliveries(authority, id)
);
CREATE TABLE session_input_routes (
    authority text NOT NULL, interpretation_authority text NOT NULL,
    interpretation_id text NOT NULL, session_id text NOT NULL, input_id text NOT NULL,
    PRIMARY KEY (authority, interpretation_authority, interpretation_id, session_id),
    FOREIGN KEY (authority, input_id) REFERENCES records(authority, id)
);
-- This projection can be stale. Search joins current records and session scopes.
CREATE TABLE segments (
    authority text NOT NULL, id text NOT NULL, body text NOT NULL,
    search tsvector GENERATED ALWAYS AS (to_tsvector('english', body)) STORED,
    PRIMARY KEY (authority, id),
    FOREIGN KEY (authority, id) REFERENCES records(authority, id)
);
CREATE INDEX segments_search ON segments USING gin(search);
CREATE INDEX records_observed ON records(authority, observed_seconds);
