CREATE TABLE IF NOT EXISTS workflow_service (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 application bytea NOT NULL, version integer NOT NULL CHECK(version=4)
);
CREATE TABLE IF NOT EXISTS workflow_grants (
 actor bytea NOT NULL, owner bytea NOT NULL, action text NOT NULL,
 allowed boolean NOT NULL, PRIMARY KEY(actor,owner,action)
);
CREATE TABLE IF NOT EXISTS workflow_definitions (
 owner bytea NOT NULL, id text NOT NULL, revision text NOT NULL,
 body bytea NOT NULL, actor bytea NOT NULL, proof bytea NOT NULL,
 PRIMARY KEY(owner,id,revision)
);
CREATE TABLE IF NOT EXISTS workflow_drafts (
 owner bytea NOT NULL, id text NOT NULL, version bigint NOT NULL,
 body bytea NOT NULL, request bytea NOT NULL, actor bytea NOT NULL,
 PRIMARY KEY(owner,id)
);
CREATE TABLE IF NOT EXISTS workflow_runs (
 owner bytea NOT NULL, id text NOT NULL, body bytea NOT NULL,
 actor bytea NOT NULL, proof bytea NOT NULL,
 accepted_at timestamptz NOT NULL DEFAULT date_trunc('milliseconds',clock_timestamp()),
 PRIMARY KEY(owner,id)
);
CREATE TABLE IF NOT EXISTS workflow_run_steps (
 owner bytea NOT NULL, run_id text NOT NULL, step_id text NOT NULL,
 input bytea NOT NULL, prompt text NOT NULL,
 submit_id text NOT NULL, claim_id text NOT NULL, complete_id text NOT NULL,
 task bytea, dispatched boolean NOT NULL DEFAULT false,
 result bytea, evidence bytea, result_actor bytea, completion_proof bytea,
 PRIMARY KEY(owner,run_id,step_id),
 FOREIGN KEY(owner,run_id) REFERENCES workflow_runs(owner,id)
);
CREATE TABLE IF NOT EXISTS workflow_coordination (
 owner bytea NOT NULL, run_id text NOT NULL,
 due_at timestamptz DEFAULT clock_timestamp(),
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),
 wake_requested boolean NOT NULL DEFAULT false,
 checked_at timestamptz,
 outcome text NOT NULL DEFAULT 'pending'
   CHECK(outcome IN ('pending','evaluated','complete','denied','reconcile_required','error')),
 PRIMARY KEY(owner,run_id),
 FOREIGN KEY(owner,run_id) REFERENCES workflow_runs(owner,id)
);
CREATE INDEX IF NOT EXISTS workflow_coordination_due
 ON workflow_coordination(due_at,owner,run_id) WHERE due_at IS NOT NULL;
ALTER TABLE workflow_coordination ADD COLUMN IF NOT EXISTS wake_requested boolean NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS workflow_sweep_health (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 checked_at timestamptz
);
CREATE TABLE IF NOT EXISTS workflow_timeout_work (
 owner bytea NOT NULL, run_id text NOT NULL, step_id text NOT NULL,
 due_at timestamptz DEFAULT clock_timestamp(),
 lease_until timestamptz NOT NULL DEFAULT '-infinity',
 generation bigint NOT NULL DEFAULT 0 CHECK(generation>=0),
 outcome text NOT NULL DEFAULT 'pending' CHECK(outcome IN ('pending','checked','closed','error')),
 PRIMARY KEY(owner,run_id,step_id),
 FOREIGN KEY(owner,run_id,step_id) REFERENCES workflow_run_steps(owner,run_id,step_id)
);
CREATE INDEX IF NOT EXISTS workflow_timeout_due ON workflow_timeout_work(due_at,owner,run_id,step_id)
 WHERE due_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS workflow_timeout_attempts (
 owner bytea NOT NULL, run_id text NOT NULL, step_id text NOT NULL, generation bigint NOT NULL,
 request bytea NOT NULL, started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 reply bytea, observation bytea, finished_at timestamptz,
 outcome text NOT NULL DEFAULT 'pending' CHECK(outcome IN ('pending','checked','closed','error')),
 PRIMARY KEY(owner,run_id,step_id,generation),
 FOREIGN KEY(owner,run_id,step_id) REFERENCES workflow_timeout_work(owner,run_id,step_id)
);
CREATE TABLE IF NOT EXISTS workflow_call_evidence (
 owner bytea NOT NULL, run_id text NOT NULL, step_id text NOT NULL,
 body bytea NOT NULL,
 PRIMARY KEY(owner,run_id,step_id),
 FOREIGN KEY(owner,run_id,step_id) REFERENCES workflow_run_steps(owner,run_id,step_id)
);
