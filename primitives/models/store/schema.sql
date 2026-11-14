CREATE TABLE IF NOT EXISTS models (
  name       text   PRIMARY KEY,
  doc        jsonb  NOT NULL,
  updated_ms bigint NOT NULL
);
CREATE TABLE IF NOT EXISTS tasks (
  task       text   PRIMARY KEY,
  doc        jsonb  NOT NULL,
  updated_ms bigint NOT NULL
);
