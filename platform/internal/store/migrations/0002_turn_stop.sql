-- The API records the engineer's stop request; the supervisor acts on it. Keeps the API free of
-- sandbox calls and makes the request durable across a supervisor restart.
ALTER TABLE turns ADD COLUMN stop_requested boolean NOT NULL DEFAULT false;
