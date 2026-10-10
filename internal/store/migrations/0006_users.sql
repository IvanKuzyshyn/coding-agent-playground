-- Users log in by name; the API token is derived from the server key and only its hash is kept.
CREATE TABLE users (
    name        text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,38}$'),
    token_hash  bytea NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);
