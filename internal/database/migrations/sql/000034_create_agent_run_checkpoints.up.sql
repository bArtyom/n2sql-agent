CREATE TABLE agent_run_checkpoints (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_run_id BIGINT NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    attempt_count INTEGER NOT NULL,
    step_number INTEGER NOT NULL,
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (agent_run_id, attempt_count, tool_call_id)
);

CREATE INDEX agent_run_checkpoints_run_idx
ON agent_run_checkpoints (agent_run_id, attempt_count, step_number, id);
