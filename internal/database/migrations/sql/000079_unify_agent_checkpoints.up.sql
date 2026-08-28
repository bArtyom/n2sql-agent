-- One durable checkpoint owns the resumable Agent graph state.
-- Older context/decision/tool checkpoint tables are intentionally removed.
DROP TABLE IF EXISTS agent_thread_contexts;
DROP TABLE IF EXISTS agent_run_contexts;
DROP TABLE IF EXISTS agent_run_decisions;
DROP TABLE IF EXISTS agent_run_checkpoints;

CREATE TABLE agent_checkpoints (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_run_id BIGINT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    conversation_id BIGINT REFERENCES conversations(id) ON DELETE CASCADE,
    attempt_count INTEGER NOT NULL CHECK (attempt_count > 0),
    step_number INTEGER NOT NULL CHECK (step_number >= 0),
    checkpoint_id TEXT NOT NULL,
    state JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (agent_run_id, attempt_count, checkpoint_id)
);

CREATE INDEX agent_checkpoints_run_idx
    ON agent_checkpoints (agent_run_id, attempt_count, step_number, id);

CREATE INDEX agent_checkpoints_thread_idx
    ON agent_checkpoints (conversation_id, created_at DESC, id DESC);
