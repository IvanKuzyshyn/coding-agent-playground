CREATE TABLE sessions (
  id                  uuid PRIMARY KEY,
  owner               text NOT NULL,
  status              text NOT NULL CHECK (status IN
                        ('pending','provisioning','idle','running','stopping','stopped','failed')),
  harness             text NOT NULL CHECK (harness IN ('claude-code','codex')),
  spec                jsonb NOT NULL,
  harness_session     text,                     -- claude session_id / codex thread_id, for resume
  container_id        text,                     -- cleanup obligation, written before create
  sandbox_token       text NOT NULL,            -- authenticates the supervisor to sandboxd
  gateway_secret_hash bytea NOT NULL UNIQUE,    -- sha256 of the per-session gateway secret
  tokens_used         bigint NOT NULL DEFAULT 0,
  error               text,
  attempts            int NOT NULL DEFAULT 0,
  next_attempt_at     timestamptz,
  idle_until          timestamptz,
  expires_at          timestamptz NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  ended_at            timestamptz
);
CREATE INDEX sessions_owner_created ON sessions (owner, created_at DESC);
CREATE INDEX sessions_active ON sessions (status) WHERE status <> 'stopped';

CREATE TABLE turns (
  session_id  uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq         int NOT NULL,
  prompt      text NOT NULL,
  status      text NOT NULL CHECK (status IN
                ('queued','running','complete','failed','stopped','limit_exceeded')),
  command_id  text,                             -- sandboxd idempotency key, reattach
  log_offset  bigint NOT NULL DEFAULT 0,        -- stdout reattach cursor
  err_offset  bigint NOT NULL DEFAULT 0,        -- stderr reattach cursor
  usage       jsonb,                            -- normalized: input/output/cached tokens, steps
  result      text,
  error       text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  started_at  timestamptz,
  ended_at    timestamptz,
  PRIMARY KEY (session_id, seq)
);

-- One ordered stream per session for attach. Global id is the cursor, so the supervisor and the
-- gateway can append concurrently without coordinating per-session sequence numbers.
CREATE TABLE session_events (
  id          bigserial PRIMARY KEY,
  session_id  uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  kind        text NOT NULL CHECK (kind IN ('lifecycle','harness','stderr','egress','system')),
  turn_seq    int,
  payload     jsonb NOT NULL,
  ts          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX session_events_session_id ON session_events (session_id, id);
