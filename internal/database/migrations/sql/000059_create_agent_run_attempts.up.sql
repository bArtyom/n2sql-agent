CREATE TABLE agent_run_attempts (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_run_id BIGINT NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    attempt_count INTEGER NOT NULL CHECK (attempt_count > 0),
    status TEXT NOT NULL CHECK (status IN (
        'running',
        'waiting_children',
        'succeeded',
        'failed',
        'timeout',
        'canceled',
        'requeued'
    )),
    error_message TEXT,
    stop_reason TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (agent_run_id, attempt_count)
);

CREATE INDEX agent_run_attempts_run_idx
ON agent_run_attempts (agent_run_id, attempt_count, id);

-- Preserve aggregate history that already exists on agent_runs when this
-- migration is introduced. A pending row with a previous attempt represents
-- lease recovery; never-started pending rows are intentionally omitted.
INSERT INTO agent_run_attempts (
    agent_run_id,
    attempt_count,
    status,
    error_message,
    stop_reason,
    started_at,
    finished_at,
    updated_at
)
SELECT
    id,
    attempt_count,
    CASE status
        WHEN 'running' THEN 'running'
        WHEN 'waiting_children' THEN 'waiting_children'
        WHEN 'succeeded' THEN 'succeeded'
        WHEN 'failed' THEN 'failed'
        WHEN 'timeout' THEN 'timeout'
        WHEN 'canceled' THEN 'canceled'
        ELSE 'requeued'
    END,
    NULLIF(error_message, ''),
    NULLIF(stop_reason, ''),
    COALESCE(started_at, created_at),
    finished_at,
    updated_at
FROM agent_runs
WHERE attempt_count > 0
ON CONFLICT (agent_run_id, attempt_count) DO NOTHING;
