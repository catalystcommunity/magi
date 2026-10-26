CREATE TABLE IF NOT EXISTS workflow_journal_binding (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    installation text NOT NULL
);
CREATE TABLE IF NOT EXISTS workflow_intents (
    installation text NOT NULL, id text NOT NULL, method text NOT NULL,
    queue text NOT NULL, request_key text NOT NULL, request bytea NOT NULL,
    provenance bytea NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','confirmed','rejected','reconcile_required')),
    first_send timestamptz, safe_until timestamptz, retention_seconds bigint,
    last_checked timestamptz, attempts bigint NOT NULL DEFAULT 0,
    receipt bytea, rejection_code bigint,
    PRIMARY KEY(installation,id), UNIQUE(installation,request_key)
);
CREATE INDEX IF NOT EXISTS workflow_pending ON workflow_intents(installation,id) WHERE state='pending';
CREATE TABLE IF NOT EXISTS workflow_results (
    installation text NOT NULL, task_uuid text NOT NULL, revision bigint NOT NULL,
    result bytea NOT NULL, evidence text NOT NULL,
    PRIMARY KEY(installation,task_uuid,revision)
);
CREATE TABLE IF NOT EXISTS workflow_human_bindings (
    installation text NOT NULL, id text NOT NULL,
    task_uuid text NOT NULL, revision bigint NOT NULL,
    binding bytea NOT NULL, completion bytea,
    PRIMARY KEY(installation,id), UNIQUE(installation,task_uuid,revision)
);
