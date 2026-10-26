CREATE TABLE IF NOT EXISTS speech_service (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    application bytea NOT NULL, version integer NOT NULL CHECK(version=1)
);
CREATE TABLE IF NOT EXISTS speech_grants (
    actor bytea NOT NULL, owner bytea NOT NULL, allowed boolean NOT NULL,
    PRIMARY KEY(actor,owner)
);
CREATE TABLE IF NOT EXISTS speech_requests (
    actor bytea NOT NULL, owner bytea NOT NULL, id text NOT NULL,
    request bytea NOT NULL, proof bytea NOT NULL, status bytea NOT NULL,
    kind text NOT NULL CHECK(kind IN ('tts','stt')),
    state text NOT NULL CHECK(state IN ('uploading','queued','running','completed','failed','cancelled')),
    accepted_at timestamptz NOT NULL, deadline timestamptz NOT NULL,
    expires_at timestamptz NOT NULL, token text NOT NULL DEFAULT '',
    lease_until timestamptz, input bytea NOT NULL DEFAULT ''::bytea,
    output bytea NOT NULL DEFAULT ''::bytea,
    PRIMARY KEY(actor,owner,id),
    CHECK(octet_length(input)<=1920000), CHECK(octet_length(output)<=5760000)
);
CREATE INDEX IF NOT EXISTS speech_queue ON speech_requests(kind,accepted_at) WHERE state='queued';
CREATE TABLE IF NOT EXISTS speech_observations (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor bytea NOT NULL, owner bytea NOT NULL, id text NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT date_trunc('milliseconds',now()), status bytea NOT NULL,
    FOREIGN KEY(actor,owner,id) REFERENCES speech_requests(actor,owner,id)
);
