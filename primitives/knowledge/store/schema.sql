-- The knowledge store (docs/knowledge.md). owners and readers are sorted
-- lists of subjects and groups. status is "current", "ended" (claims),
-- "merged" (entities), or "retracted". search_text is kept by the store.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS entities (
  id          text PRIMARY KEY,
  kind        text NOT NULL,
  label       text NOT NULL,
  aliases     text[] NOT NULL DEFAULT '{}',
  summary     text NOT NULL DEFAULT '',
  properties  jsonb NOT NULL DEFAULT '{}',
  ext_system  text,
  ext_locator text,
  ext_context text,
  owners      text[] NOT NULL,
  readers     text[] NOT NULL DEFAULT '{}',
  confidence  double precision NOT NULL,
  status      text NOT NULL DEFAULT 'current',
  merged_into text,
  created_ms  bigint NOT NULL,
  updated_ms  bigint NOT NULL,
  search_text text NOT NULL DEFAULT '',
  search      tsvector GENERATED ALWAYS AS (to_tsvector('english', search_text)) STORED
);
CREATE INDEX IF NOT EXISTS entities_owners ON entities USING gin (owners);
CREATE INDEX IF NOT EXISTS entities_readers ON entities USING gin (readers);
CREATE INDEX IF NOT EXISTS entities_search ON entities USING gin (search);
CREATE INDEX IF NOT EXISTS entities_label_trgm ON entities USING gin (lower(label) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS entities_external ON entities (ext_system, ext_locator) WHERE ext_system IS NOT NULL;

CREATE TABLE IF NOT EXISTS claims (
  id            text PRIMARY KEY,
  subject       text NOT NULL REFERENCES entities(id),
  relation      text NOT NULL,
  object        text REFERENCES entities(id),
  value         text,
  valid_from_ms bigint,
  valid_to_ms   bigint,
  status        text NOT NULL DEFAULT 'current',
  replaced_by   text,
  confidence    double precision NOT NULL,
  authored_by   text,
  derived_from  text[] NOT NULL DEFAULT '{}',
  stale         boolean NOT NULL DEFAULT false,
  owners        text[] NOT NULL,
  readers       text[] NOT NULL DEFAULT '{}',
  created_ms    bigint NOT NULL,
  updated_ms    bigint NOT NULL,
  search_text   text NOT NULL DEFAULT '',
  search        tsvector GENERATED ALWAYS AS (to_tsvector('english', search_text)) STORED,
  CHECK ((object IS NULL) <> (value IS NULL))
);
CREATE INDEX IF NOT EXISTS claims_subject ON claims (subject);
CREATE INDEX IF NOT EXISTS claims_object ON claims (object);
CREATE INDEX IF NOT EXISTS claims_owners ON claims USING gin (owners);
CREATE INDEX IF NOT EXISTS claims_search ON claims USING gin (search);
CREATE INDEX IF NOT EXISTS claims_derived ON claims USING gin (derived_from);

CREATE TABLE IF NOT EXISTS episodes (
  id           text PRIMARY KEY,
  kind         text NOT NULL,
  title        text NOT NULL,
  summary      text NOT NULL DEFAULT '',
  started_ms   bigint NOT NULL,
  ended_ms     bigint,
  participants text[] NOT NULL DEFAULT '{}',
  entities     text[] NOT NULL DEFAULT '{}',
  refs         jsonb NOT NULL DEFAULT '[]',
  procedure    text,
  outcome      text,
  owners       text[] NOT NULL,
  readers      text[] NOT NULL DEFAULT '{}',
  status       text NOT NULL DEFAULT 'current',
  created_ms   bigint NOT NULL,
  updated_ms   bigint NOT NULL,
  search_text  text NOT NULL DEFAULT '',
  search       tsvector GENERATED ALWAYS AS (to_tsvector('english', search_text)) STORED
);
CREATE INDEX IF NOT EXISTS episodes_owners ON episodes USING gin (owners);
CREATE INDEX IF NOT EXISTS episodes_entities ON episodes USING gin (entities);
CREATE INDEX IF NOT EXISTS episodes_started ON episodes (started_ms);
CREATE INDEX IF NOT EXISTS episodes_search ON episodes USING gin (search);

CREATE TABLE IF NOT EXISTS procedures (
  id          text PRIMARY KEY,
  name        text NOT NULL,
  goal        text NOT NULL,
  inputs      jsonb NOT NULL DEFAULT '{}',
  steps       text NOT NULL,
  definition  text,
  revision    bigint NOT NULL,
  status      text NOT NULL,
  entities    text[] NOT NULL DEFAULT '{}',
  owners      text[] NOT NULL,
  readers     text[] NOT NULL DEFAULT '{}',
  retracted   boolean NOT NULL DEFAULT false,
  created_ms  bigint NOT NULL,
  updated_ms  bigint NOT NULL,
  search_text text NOT NULL DEFAULT '',
  search      tsvector GENERATED ALWAYS AS (to_tsvector('english', search_text)) STORED
);
CREATE INDEX IF NOT EXISTS procedures_name ON procedures (name, revision);
CREATE INDEX IF NOT EXISTS procedures_owners ON procedures USING gin (owners);
CREATE INDEX IF NOT EXISTS procedures_search ON procedures USING gin (search);

CREATE TABLE IF NOT EXISTS evidence (
  id         text PRIMARY KEY,
  subject    text NOT NULL,
  ref_kind   text NOT NULL,
  ref        text NOT NULL,
  ref_title  text,
  stance     text NOT NULL,
  note       text,
  owners     text[] NOT NULL,
  retracted  boolean NOT NULL DEFAULT false,
  created_ms bigint NOT NULL,
  UNIQUE (subject, ref_kind, ref)
);
CREATE INDEX IF NOT EXISTS evidence_subject ON evidence (subject);

CREATE TABLE IF NOT EXISTS changes (
  id        text PRIMARY KEY,
  record    text NOT NULL,
  operation text NOT NULL,
  actor     text NOT NULL,
  reason    text,
  before    text,
  after     text,
  utterance text,
  owners    text[] NOT NULL,
  at_ms     bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS changes_record ON changes (record, at_ms);
CREATE INDEX IF NOT EXISTS changes_at ON changes (at_ms);

CREATE TABLE IF NOT EXISTS disputes (
  id         text PRIMARY KEY,
  record     text NOT NULL,
  by_subject text NOT NULL,
  reason     text NOT NULL,
  proposed   text,
  state      text NOT NULL,
  resolution text,
  owners     text[] NOT NULL,
  created_ms bigint NOT NULL,
  updated_ms bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS disputes_owners ON disputes USING gin (owners);

CREATE TABLE IF NOT EXISTS groups (
  id         text PRIMARY KEY,
  label      text NOT NULL,
  members    text[] NOT NULL,
  owners     text[] NOT NULL,
  updated_ms bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS groups_members ON groups USING gin (members);
