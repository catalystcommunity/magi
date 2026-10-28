CREATE TABLE IF NOT EXISTS workflow_definitions (
  name       text   NOT NULL,
  revision   bigint NOT NULL,
  owner      text   NOT NULL,
  doc        jsonb  NOT NULL,
  created_ms bigint NOT NULL,
  PRIMARY KEY (name, revision)
);

CREATE TABLE IF NOT EXISTS workflow_runs (
  id         text   PRIMARY KEY,
  owner      text   NOT NULL,
  definition text   NOT NULL,
  status     text   NOT NULL,
  start_key  text,
  doc        jsonb  NOT NULL,
  created_ms bigint NOT NULL,
  updated_ms bigint NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS workflow_runs_owner_key ON workflow_runs (owner, start_key) WHERE start_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS workflow_runs_owner_created ON workflow_runs (owner, created_ms DESC);

-- One row per task step attempt. state: submit (not yet confirmed in the
-- queue), queued, claimed, finish (queue task must be closed), done.
CREATE TABLE IF NOT EXISTS workflow_tasks (
  key         text   PRIMARY KEY,
  run_id      text   NOT NULL REFERENCES workflow_runs (id),
  idx         int    NOT NULL,
  step        text   NOT NULL,
  queue       text   NOT NULL,
  state       text   NOT NULL,
  task_uuid   text   NOT NULL DEFAULT '',
  revision    bigint NOT NULL DEFAULT 0,
  claim       text   NOT NULL DEFAULT '',
  worker      text   NOT NULL DEFAULT '',
  lease_until bigint NOT NULL DEFAULT 0,
  updated_ms  bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS workflow_tasks_run ON workflow_tasks (run_id);
CREATE INDEX IF NOT EXISTS workflow_tasks_open ON workflow_tasks (state, updated_ms) WHERE state <> 'done';
