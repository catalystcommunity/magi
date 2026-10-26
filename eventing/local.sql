CREATE TABLE IF NOT EXISTS event_local_outbox (
    owner text NOT NULL, destination text NOT NULL, id text NOT NULL,
    target bytea NOT NULL, body bytea NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','attempting','retry_due','accepted','attention')),
    attempt text, attempts integer NOT NULL DEFAULT 0,
    due timestamptz NOT NULL DEFAULT clock_timestamp(), receipt bytea, last_error text,
    PRIMARY KEY(owner,destination,id)
);
CREATE INDEX IF NOT EXISTS event_local_due ON event_local_outbox(owner,due) WHERE state IN ('pending','attempting','retry_due');
CREATE TABLE IF NOT EXISTS event_local_inbox (
    owner text NOT NULL, sender text NOT NULL, id text NOT NULL,
    body bytea NOT NULL, receipt bytea NOT NULL,
    state text NOT NULL DEFAULT 'accepted' CHECK(state IN ('accepted','completed','outcome_unknown')),
    reconciliation text,
    PRIMARY KEY(owner,sender,id)
);
