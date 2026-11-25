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

-- The voice prints of a person for one speaker model (speaker.go). The
-- three arrays have one element for each print. sample is speech of the
-- person, for the extraction of that voice from a mixture.
CREATE TABLE IF NOT EXISTS speaker_profiles (
    owner       text     NOT NULL,
    model       text     NOT NULL,
    vectors     bytea[]  NOT NULL DEFAULT '{}',
    sources     text[]   NOT NULL DEFAULT '{}',
    created     bigint[] NOT NULL DEFAULT '{}',
    sample      bytea,
    sample_type text     NOT NULL DEFAULT '',
    updated_ms  bigint   NOT NULL,
    PRIMARY KEY (owner, model)
);
