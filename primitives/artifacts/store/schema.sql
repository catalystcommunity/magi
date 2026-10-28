CREATE TABLE IF NOT EXISTS artifacts (
  digest     text   NOT NULL,
  owner      text   NOT NULL,
  media_type text   NOT NULL,
  size       bigint NOT NULL,
  labels     jsonb  NOT NULL DEFAULT '{}'::jsonb,
  created_ms bigint NOT NULL,
  PRIMARY KEY (digest, owner)
);
CREATE INDEX IF NOT EXISTS artifacts_owner_created ON artifacts (owner, created_ms DESC);
CREATE INDEX IF NOT EXISTS artifacts_labels ON artifacts USING gin (labels);
