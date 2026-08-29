DROP INDEX IF EXISTS agent_runs_active_root_conversation_idx;

ALTER TABLE agent_run_attempts
    DROP CONSTRAINT IF EXISTS agent_run_attempts_status_check,
    ADD CONSTRAINT agent_run_attempts_status_check
        CHECK (status IN (
            'running',
            'waiting_children',
            'succeeded',
            'failed',
            'timeout',
            'canceled',
            'requeued'
        ));

ALTER TABLE agent_runs
    DROP CONSTRAINT IF EXISTS agent_runs_status_check,
    ADD CONSTRAINT agent_runs_status_check
        CHECK (status IN (
            'pending',
            'running',
            'waiting_children',
            'waiting_approval',
            'succeeded',
            'failed',
            'timeout',
            'canceled'
        ));
