CREATE TABLE agent_run_decisions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_run_id BIGINT NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    attempt_count INTEGER NOT NULL,
    step_number INTEGER NOT NULL,
    decision_id TEXT NOT NULL,
    tool_calls JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (agent_run_id, attempt_count, decision_id)
);

CREATE INDEX agent_run_decisions_run_idx
ON agent_run_decisions (agent_run_id, attempt_count, step_number, id);
