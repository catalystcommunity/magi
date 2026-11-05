-- The chat model before the graph. Its tables keep their records for
-- import-legacy under the legacy_ names.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
             WHERE table_schema = current_schema() AND table_name = 'utterances' AND column_name = 'conversation') THEN
    ALTER TABLE utterances RENAME TO legacy_utterances;
    ALTER TABLE sessions RENAME TO legacy_sessions;
    ALTER TABLE conversations RENAME TO legacy_conversations;
    ALTER TABLE annotations DROP CONSTRAINT IF EXISTS annotations_utterance_fkey;
  END IF;
END $$;

-- magi_principals is the list of readers that the current transaction
-- reads as. '*' reads all. magi_via, when set, is a persona that must also
-- be a reader. The service sets both with set_config(..., true).
CREATE OR REPLACE FUNCTION magi_principals() RETURNS text[] LANGUAGE sql STABLE AS $$
  SELECT COALESCE(ARRAY(SELECT jsonb_array_elements_text(NULLIF(current_setting('magi.principals', true), '')::jsonb)), '{}')
$$;
CREATE OR REPLACE FUNCTION magi_can_read(readers text[]) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT ('*' = ANY(magi_principals()) OR readers && magi_principals())
     AND (COALESCE(current_setting('magi.via', true), '') = '' OR current_setting('magi.via', true) = ANY(readers))
$$;

CREATE TABLE IF NOT EXISTS personas (
  id         text   PRIMARY KEY,
  name       text   NOT NULL,
  sharing    text   NOT NULL,
  members    text[] NOT NULL DEFAULT '{}',
  updated_ms bigint NOT NULL
);

CREATE TABLE IF NOT EXISTS streams (
  id         text   PRIMARY KEY,
  person     text   NOT NULL,
  persona    text   NOT NULL REFERENCES personas (id),
  last_seq   bigint NOT NULL DEFAULT 0,
  created_ms bigint NOT NULL,
  updated_ms bigint NOT NULL,
  UNIQUE (person, persona)
);

CREATE TABLE IF NOT EXISTS utterances (
  id                    text   PRIMARY KEY,
  stream                text   NOT NULL REFERENCES streams (id),
  seq                   bigint NOT NULL,
  author                text   NOT NULL,
  author_kind           text   NOT NULL,
  modality              text   NOT NULL,
  text                  text   NOT NULL,
  audio                 text,
  transcript_confidence double precision,
  attachments           jsonb  NOT NULL DEFAULT '[]'::jsonb,
  generated_by          text,
  visibility            text   NOT NULL DEFAULT 'private',
  visibility_by         text   NOT NULL DEFAULT '',
  visibility_confidence double precision,
  readers               text[] NOT NULL,
  classified            boolean NOT NULL DEFAULT false,
  created_ms            bigint NOT NULL,
  search                tsvector GENERATED ALWAYS AS (to_tsvector('english', text)) STORED,
  UNIQUE (stream, seq)
);
CREATE INDEX IF NOT EXISTS utterances_readers ON utterances USING gin (readers);
CREATE INDEX IF NOT EXISTS utterances_search ON utterances USING gin (search);
ALTER TABLE utterances ADD COLUMN IF NOT EXISTS claimed_until bigint NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS utterances_unclassified ON utterances (created_ms) WHERE NOT classified;

CREATE TABLE IF NOT EXISTS links (
  id          text   PRIMARY KEY,
  utterance   text   NOT NULL REFERENCES utterances (id),
  span_start  bigint,
  span_end    bigint,
  weight      double precision NOT NULL,
  target_kind text   NOT NULL,
  target_ref  text   NOT NULL,
  target_title text,
  kind        text   NOT NULL,
  confidence  double precision NOT NULL,
  author      text   NOT NULL,
  note        text,
  state       text   NOT NULL,
  replaces    text,
  created_ms  bigint NOT NULL
);
ALTER TABLE links ADD COLUMN IF NOT EXISTS target_start bigint;
ALTER TABLE links ADD COLUMN IF NOT EXISTS target_end bigint;
CREATE INDEX IF NOT EXISTS links_utterance ON links (utterance);
CREATE INDEX IF NOT EXISTS links_target ON links (target_kind, target_ref);

-- History of visibility decisions.
CREATE TABLE IF NOT EXISTS visibility_changes (
  id         text   PRIMARY KEY,
  utterance  text   NOT NULL REFERENCES utterances (id),
  visibility text   NOT NULL,
  author     text   NOT NULL,
  confidence double precision,
  note       text,
  created_ms bigint NOT NULL
);

CREATE TABLE IF NOT EXISTS reviews (
  id          text   PRIMARY KEY,
  reviewer    text   NOT NULL,
  task        text   NOT NULL,
  utterance   text   NOT NULL REFERENCES utterances (id),
  link        text,
  question    text   NOT NULL,
  proposed    text   NOT NULL,
  choices     text[] NOT NULL,
  confidence  double precision NOT NULL,
  status      text   NOT NULL,
  answer      text,
  note        text,
  created_ms  bigint NOT NULL,
  answered_ms bigint
);
CREATE INDEX IF NOT EXISTS reviews_reviewer ON reviews (reviewer, status, created_ms);

CREATE TABLE IF NOT EXISTS examples (
  reviewer   text   NOT NULL,
  task       text   NOT NULL,
  utterance  text   NOT NULL REFERENCES utterances (id),
  proposed   text   NOT NULL,
  label      text   NOT NULL,
  note       text,
  created_ms bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS examples_reviewer ON examples (reviewer, task, created_ms DESC);

CREATE TABLE IF NOT EXISTS calibration (
  reviewer  text   NOT NULL,
  task      text   NOT NULL,
  threshold double precision NOT NULL,
  accepted  bigint NOT NULL DEFAULT 0,
  corrected bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (reviewer, task)
);

CREATE TABLE IF NOT EXISTS annotations (
  id         text   PRIMARY KEY,
  utterance  text   NOT NULL,
  kind       text   NOT NULL,
  author     text   NOT NULL,
  body       text   NOT NULL,
  created_ms bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS annotations_utterance ON annotations (utterance, created_ms);

-- Row-level security. FORCE applies the policies to the table owner too,
-- which is the role that the service connects as.
ALTER TABLE utterances ENABLE ROW LEVEL SECURITY;
ALTER TABLE utterances FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS utterances_read ON utterances;
CREATE POLICY utterances_read ON utterances FOR SELECT USING (magi_can_read(readers));
DROP POLICY IF EXISTS utterances_insert ON utterances;
CREATE POLICY utterances_insert ON utterances FOR INSERT WITH CHECK (true);
DROP POLICY IF EXISTS utterances_update ON utterances;
CREATE POLICY utterances_update ON utterances FOR UPDATE USING (magi_can_read(readers)) WITH CHECK (true);

ALTER TABLE links ENABLE ROW LEVEL SECURITY;
ALTER TABLE links FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS links_read ON links;
CREATE POLICY links_read ON links FOR SELECT USING (
  EXISTS (SELECT 1 FROM utterances u WHERE u.id = links.utterance)
  AND (target_kind <> 'utterance' OR EXISTS (SELECT 1 FROM utterances t WHERE t.id = links.target_ref)));
DROP POLICY IF EXISTS links_insert ON links;
CREATE POLICY links_insert ON links FOR INSERT WITH CHECK (true);
DROP POLICY IF EXISTS links_update ON links;
CREATE POLICY links_update ON links FOR UPDATE USING (EXISTS (SELECT 1 FROM utterances u WHERE u.id = links.utterance)) WITH CHECK (true);

ALTER TABLE reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE reviews FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS reviews_all ON reviews;
CREATE POLICY reviews_all ON reviews USING ('*' = ANY(magi_principals()) OR reviewer = ANY(magi_principals())) WITH CHECK (true);

ALTER TABLE examples ENABLE ROW LEVEL SECURITY;
ALTER TABLE examples FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS examples_all ON examples;
CREATE POLICY examples_all ON examples USING ('*' = ANY(magi_principals()) OR reviewer = ANY(magi_principals())) WITH CHECK (true);
