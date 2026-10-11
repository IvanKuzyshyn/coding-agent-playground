-- 0004 ran while a server built before it was still running; that server treated 'failed' as
-- "needs cleanup" and moved the converted rows back to 'stopped'. Convert them again. `make up`
-- now stops the server before migrating.
UPDATE sessions SET status = 'failed' WHERE status = 'stopped' AND error IS NOT NULL;
