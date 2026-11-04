-- The household lexicon. key is lower(written).
CREATE TABLE IF NOT EXISTS lexicon (
    key        text PRIMARY KEY,
    written    text NOT NULL,
    spoken     text,
    ipa        text,
    heard_as   text[] NOT NULL DEFAULT '{}',
    note       text,
    updated_by text NOT NULL,
    updated_ms bigint NOT NULL
);
