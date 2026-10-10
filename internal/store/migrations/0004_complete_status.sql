-- complete, failed and stopped are terminal statuses reached after the sandbox is destroyed.
-- While a session is stopping, final_status records which one it ends in (NULL: derive from the
-- last finished turn at cleanup).
ALTER TABLE sessions DROP CONSTRAINT sessions_status_check;
ALTER TABLE sessions ADD CONSTRAINT sessions_status_check CHECK (status IN
  ('pending','provisioning','idle','running','stopping','complete','failed','stopped'));
ALTER TABLE sessions ADD COLUMN final_status text CHECK (final_status IN ('complete','failed','stopped'));

-- Before this migration, failed sessions were cleaned up into 'stopped' with an error.
UPDATE sessions SET status = 'failed' WHERE status = 'stopped' AND error IS NOT NULL;
UPDATE sessions SET ended_at = coalesce(ended_at, updated_at) WHERE status = 'failed';

DROP INDEX sessions_active;
CREATE INDEX sessions_active ON sessions (status) WHERE status NOT IN ('complete','failed','stopped');
