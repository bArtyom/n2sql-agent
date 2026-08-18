ALTER TABLE agent_runs
    ADD COLUMN lease_until TIMESTAMPTZ,
    ADD COLUMN heartbeat_at TIMESTAMPTZ;

CREATE INDEX agent_runs_expired_running_idx
ON agent_runs (lease_until, id)
WHERE status = 'running' AND lease_until IS NOT NULL;
