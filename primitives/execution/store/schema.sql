CREATE TABLE IF NOT EXISTS environments (
  id           text PRIMARY KEY,
  owner        text NOT NULL,
  backend      text NOT NULL,
  spec         jsonb NOT NULL,
  status       text NOT NULL,
  message      text NOT NULL DEFAULT '',
  held         boolean NOT NULL DEFAULT false,
  created_ms   bigint NOT NULL,
  expires_ms   bigint NOT NULL,
  destroyed_ms bigint NOT NULL DEFAULT 0,
  endpoint     text NOT NULL DEFAULT '',
  token        text NOT NULL
);
CREATE INDEX IF NOT EXISTS environments_owner ON environments (owner, created_ms DESC);
CREATE INDEX IF NOT EXISTS environments_live ON environments (expires_ms) WHERE status <> 'destroyed';
