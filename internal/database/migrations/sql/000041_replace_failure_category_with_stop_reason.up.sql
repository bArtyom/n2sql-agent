ALTER TABLE agent_runs
    DROP CONSTRAINT IF EXISTS agent_runs_failure_category_check;

ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS failure_category;

ALTER TABLE agent_runs
    ADD COLUMN stop_reason TEXT;

ALTER TABLE agent_runs
    DROP CONSTRAINT IF EXISTS agent_runs_status_check;

ALTER TABLE agent_runs
    ADD CONSTRAINT agent_runs_status_check
    CHECK (status IN ('pending', 'running', 'waiting_children', 'succeeded', 'failed', 'timeout', 'canceled'));
