CREATE TABLE agent_thread_contexts (
    conversation_id BIGINT PRIMARY KEY REFERENCES conversations (id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    last_step INTEGER NOT NULL CHECK (last_step >= 0),
    last_run_id TEXT NOT NULL,
    messages JSONB NOT NULL,
    summary_text TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX agent_thread_contexts_updated_idx
ON agent_thread_contexts (updated_at);
