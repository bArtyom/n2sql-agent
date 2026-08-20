ALTER TABLE agent_runs
    ADD COLUMN failure_category TEXT;

ALTER TABLE agent_runs
    ADD CONSTRAINT agent_runs_failure_category_check
    CHECK (failure_category IS NULL OR failure_category IN (
        'model_failed',
        'tool_failed',
        'timeout',
        'canceled',
        'step_limit_exceeded',
        'validation_failed',
        'internal_failed'
    ));
