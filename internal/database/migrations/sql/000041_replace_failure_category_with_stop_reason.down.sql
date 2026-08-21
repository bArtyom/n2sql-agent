ALTER TABLE agent_runs
    DROP CONSTRAINT IF EXISTS agent_runs_status_check;

ALTER TABLE agent_runs
    ADD CONSTRAINT agent_runs_status_check
    CHECK (status IN ('pending', 'running', 'waiting_children', 'succeeded', 'failed', 'canceled'));

ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS stop_reason;

ALTER TABLE agent_runs
    ADD COLUMN failure_category TEXT;
