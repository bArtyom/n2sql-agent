ALTER TABLE agent_runs
    ADD COLUMN lease_token TEXT;

UPDATE agent_runs
SET lease_token = md5(random()::text || clock_timestamp()::text || id::text)
WHERE status = 'running';

CREATE INDEX agent_runs_lease_token_idx
ON agent_runs (id, lease_token)
WHERE status = 'running';
