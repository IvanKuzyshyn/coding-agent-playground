-- Engineers name their runs; names need not be unique (sessions are identified by id).
ALTER TABLE sessions ADD COLUMN name text;
UPDATE sessions s SET name = left(regexp_replace(coalesce(
    (SELECT prompt FROM turns t WHERE t.session_id = s.id AND t.seq = 1), 'session'), '\s+', ' ', 'g'), 120);
ALTER TABLE sessions ALTER COLUMN name SET NOT NULL;

-- API-equivalent cost estimate accumulated by the gateway from list prices. cost_complete turns
-- false once a request used a model missing from the price table.
ALTER TABLE sessions ADD COLUMN cost_usd numeric(14, 6) NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN cost_complete boolean NOT NULL DEFAULT true;
