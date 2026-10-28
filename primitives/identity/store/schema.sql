-- Pending browser logins. Secrets (login_id, start token, lk_state, browser
-- binding) are stored only as SHA-256 hex digests.
CREATE TABLE IF NOT EXISTS identity_logins (
  login_hash   text   PRIMARY KEY,
  start_hash   text   NOT NULL UNIQUE,
  state_hash   text   NOT NULL UNIQUE,
  identity     text   NOT NULL,
  domain       text   NOT NULL,
  client       text   NOT NULL DEFAULT '',
  nonce        text   NOT NULL,
  api_base     text   NOT NULL,
  callback_url text   NOT NULL,
  idp_url      text   NOT NULL,
  binding_hash text   NOT NULL DEFAULT '',
  status       text   NOT NULL,
  message      text   NOT NULL DEFAULT '',
  subject      text   NOT NULL DEFAULT '',
  session_id   text   NOT NULL DEFAULT '',
  created_ms   bigint NOT NULL,
  expires_ms   bigint NOT NULL
);
ALTER TABLE identity_logins DROP COLUMN IF EXISTS user_code;
CREATE INDEX IF NOT EXISTS identity_logins_expires ON identity_logins (expires_ms);

-- Login sessions. A user token can be refreshed until expires_ms.
CREATE TABLE IF NOT EXISTS identity_sessions (
  session_id       text   PRIMARY KEY,
  subject          text   NOT NULL,
  user_id          text   NOT NULL,
  domain           text   NOT NULL,
  authenticated_ms bigint NOT NULL,
  expires_ms       bigint NOT NULL,
  revoked_ms       bigint
);
CREATE INDEX IF NOT EXISTS identity_sessions_subject ON identity_sessions (subject, expires_ms DESC);
ALTER TABLE identity_sessions ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE identity_logins ADD COLUMN IF NOT EXISTS return_url text NOT NULL DEFAULT '';
