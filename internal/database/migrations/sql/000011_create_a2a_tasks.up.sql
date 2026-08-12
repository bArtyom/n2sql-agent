CREATE TABLE a2a_tasks (
    id TEXT PRIMARY KEY CHECK (length(BTRIM(id)) BETWEEN 1 AND 128),
    administrator_id BIGINT NOT NULL REFERENCES administrators (id),
    knowledge_base_id BIGINT NOT NULL REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    message TEXT NOT NULL CHECK (length(BTRIM(message)) BETWEEN 1 AND 8000),
    top_k INTEGER NOT NULL CHECK (top_k BETWEEN 1 AND 20),
    status TEXT NOT NULL CHECK (status IN ('submitted', 'working', 'completed', 'failed')),
    response JSONB,
    error_code TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX a2a_tasks_claim_idx
    ON a2a_tasks (status, lease_until, created_at, id);

CREATE INDEX a2a_tasks_knowledge_base_idx
    ON a2a_tasks (knowledge_base_id, created_at DESC, id DESC);

CREATE TRIGGER a2a_tasks_refresh_updated_at
BEFORE UPDATE ON a2a_tasks
FOR EACH ROW
EXECUTE FUNCTION refresh_updated_at();
