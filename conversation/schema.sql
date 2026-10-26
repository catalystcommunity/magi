CREATE TABLE IF NOT EXISTS conversation_service (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    application bytea NOT NULL, version integer NOT NULL CHECK(version=1)
);
CREATE TABLE IF NOT EXISTS conversation_grants (
    actor bytea NOT NULL, owner bytea NOT NULL, action text NOT NULL CHECK(action IN ('read','write')),
    allowed boolean NOT NULL, PRIMARY KEY(actor,owner,action)
);
CREATE TABLE IF NOT EXISTS conversation_records (
    sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    owner bytea NOT NULL, ref bytea NOT NULL, actor bytea NOT NULL,
    body bytea NOT NULL CHECK(octet_length(body)<=8192), proof bytea NOT NULL,
    receipt bytea NOT NULL, PRIMARY KEY(owner,ref)
);
CREATE TABLE IF NOT EXISTS conversation_sessions (
    owner bytea NOT NULL, session_id text NOT NULL, ref bytea NOT NULL,
    sequence bigint NOT NULL REFERENCES conversation_records(sequence),
    PRIMARY KEY(owner,session_id,ref), FOREIGN KEY(owner,ref) REFERENCES conversation_records(owner,ref)
);
CREATE INDEX IF NOT EXISTS conversation_history ON conversation_sessions(owner,session_id,sequence);
CREATE TABLE IF NOT EXISTS conversation_edges (
    owner bytea NOT NULL, source bytea NOT NULL, target bytea NOT NULL,
    PRIMARY KEY(owner,source,target),
    FOREIGN KEY(owner,source) REFERENCES conversation_records(owner,ref),
    FOREIGN KEY(owner,target) REFERENCES conversation_records(owner,ref)
);
