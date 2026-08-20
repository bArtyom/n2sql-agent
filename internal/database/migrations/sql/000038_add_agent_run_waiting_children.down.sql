ALTER TABLE agent_runs
    DROP CONSTRAINT agent_runs_status_check,
    ADD CONSTRAINT agent_runs_status_check
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'canceled'));
