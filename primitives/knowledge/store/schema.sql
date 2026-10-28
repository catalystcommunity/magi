CREATE TABLE IF NOT EXISTS nodes (
  id          text PRIMARY KEY,
  owner       text NOT NULL,
  kind        text NOT NULL,
  label       text NOT NULL,
  summary     text NOT NULL DEFAULT '',
  properties  jsonb NOT NULL DEFAULT '{}',
  ext_system  text,
  ext_locator text,
  ext_context text,
  scope       text NOT NULL,
  authored_by text,
  confidence  double precision NOT NULL,
  created_ms  bigint NOT NULL,
  updated_ms  bigint NOT NULL,
  search_text text NOT NULL DEFAULT '',
  search      tsvector GENERATED ALWAYS AS (to_tsvector('english', search_text)) STORED
);
CREATE UNIQUE INDEX IF NOT EXISTS nodes_external ON nodes (owner, ext_system, ext_locator) WHERE ext_system IS NOT NULL;
CREATE INDEX IF NOT EXISTS nodes_owner_kind ON nodes (owner, kind);
CREATE INDEX IF NOT EXISTS nodes_owner_updated ON nodes (owner, updated_ms DESC);
CREATE INDEX IF NOT EXISTS nodes_search ON nodes USING gin (search);

CREATE TABLE IF NOT EXISTS edges (
  id         text PRIMARY KEY,
  owner      text NOT NULL,
  from_node  text NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  to_node    text NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  relation   text NOT NULL,
  properties jsonb NOT NULL DEFAULT '{}',
  confidence double precision NOT NULL,
  created_ms bigint NOT NULL,
  UNIQUE (owner, from_node, to_node, relation)
);
CREATE INDEX IF NOT EXISTS edges_from ON edges (from_node);
CREATE INDEX IF NOT EXISTS edges_to ON edges (to_node);

CREATE TABLE IF NOT EXISTS evidence (
  id         text PRIMARY KEY,
  owner      text NOT NULL,
  subject    text NOT NULL,
  ref_kind   text NOT NULL,
  ref        text NOT NULL,
  stance     text NOT NULL,
  note       text,
  created_ms bigint NOT NULL,
  UNIQUE (subject, ref_kind, ref)
);
