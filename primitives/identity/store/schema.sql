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
ALTER TABLE identity_logins ADD COLUMN IF NOT EXISTS code_hash text NOT NULL DEFAULT '';

-- Machine subjects (inference:<name>, runner:<name>) that renew their own
-- tokens. A token issued before not_before_ms cannot renew: set it to the
-- current time to stop a machine at the expiry of its current token.
CREATE TABLE IF NOT EXISTS identity_machines (
  subject       text   PRIMARY KEY,
  first_ms      bigint NOT NULL,
  renewed_ms    bigint NOT NULL,
  not_before_ms bigint NOT NULL DEFAULT 0
);

-- Act-as grant requests that wait for the browser callback. The nonce is
-- stored only as a SHA-256 hex digest.
CREATE TABLE IF NOT EXISTS identity_grant_requests (
  nonce_hash  text   PRIMARY KEY,
  owner       text   NOT NULL,
  domain      text   NOT NULL,
  user_id     text   NOT NULL,
  audience    text   NOT NULL,
  return_url  text   NOT NULL DEFAULT '',
  created_ms  bigint NOT NULL,
  expires_ms  bigint NOT NULL,
  grant_id    text   NOT NULL DEFAULT ''
);

-- Act-as grants that Magi holds. audience is "<user_id>@<domain>/<app_id>".
-- grant_bytes is the newest signed grant (CBOR SignedActAsGrant). A grant
-- is not a secret: only Magi's application keys can present it.
CREATE TABLE IF NOT EXISTS identity_grants (
  grant_id           text   PRIMARY KEY,
  owner              text   NOT NULL,
  domain             text   NOT NULL,
  audience           text   NOT NULL,
  scopes             text[] NOT NULL,
  grant_bytes        bytea  NOT NULL,
  expires_ms         bigint NOT NULL,
  renewable_until_ms bigint NOT NULL,
  created_ms         bigint NOT NULL,
  updated_ms         bigint NOT NULL,
  state              text   NOT NULL DEFAULT 'active'
);
CREATE INDEX IF NOT EXISTS identity_grants_owner ON identity_grants (owner, audience, created_ms DESC);

-- Credentials that people gave Magi for systems without act-as grants.
-- value_enc is AES-256-GCM (nonce || ciphertext); the additional data is
-- "<owner>\n<system>".
CREATE TABLE IF NOT EXISTS identity_credentials (
  owner      text   NOT NULL,
  system     text   NOT NULL,
  label      text   NOT NULL DEFAULT '',
  value_enc  bytea  NOT NULL,
  revision   bigint NOT NULL,
  expires_ms bigint NOT NULL DEFAULT 0,
  updated_ms bigint NOT NULL,
  PRIMARY KEY (owner, system)
);
