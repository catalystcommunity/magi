CREATE TABLE IF NOT EXISTS conversations (
  id           text   PRIMARY KEY,
  title        text   NOT NULL,
  owner        text   NOT NULL,
  participants text[] NOT NULL DEFAULT '{}',
  created_ms   bigint NOT NULL,
  updated_ms   bigint NOT NULL,
  last_seq     bigint NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS conversations_owner ON conversations (owner, updated_ms DESC);
CREATE INDEX IF NOT EXISTS conversations_participants ON conversations USING gin (participants);

CREATE TABLE IF NOT EXISTS utterances (
  id                    text   PRIMARY KEY,
  conversation          text   NOT NULL REFERENCES conversations (id),
  seq                   bigint NOT NULL,
  author                text   NOT NULL,
  author_kind           text   NOT NULL,
  modality              text   NOT NULL,
  text                  text   NOT NULL,
  audio                 text,
  transcript_confidence double precision,
  reply_to              text,
  sessions              text[] NOT NULL DEFAULT '{}',
  attachments           jsonb  NOT NULL DEFAULT '[]'::jsonb,
  created_ms            bigint NOT NULL,
  UNIQUE (conversation, seq)
);

CREATE TABLE IF NOT EXISTS sessions (
  id           text   PRIMARY KEY,
  conversation text   NOT NULL REFERENCES conversations (id),
  title        text   NOT NULL,
  status       text   NOT NULL,
  refs         jsonb  NOT NULL DEFAULT '[]'::jsonb,
  created_ms   bigint NOT NULL,
  updated_ms   bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_conversation ON sessions (conversation, created_ms);

CREATE TABLE IF NOT EXISTS annotations (
  id         text   PRIMARY KEY,
  utterance  text   NOT NULL REFERENCES utterances (id),
  kind       text   NOT NULL,
  author     text   NOT NULL,
  body       text   NOT NULL,
  created_ms bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS annotations_utterance ON annotations (utterance, created_ms);
