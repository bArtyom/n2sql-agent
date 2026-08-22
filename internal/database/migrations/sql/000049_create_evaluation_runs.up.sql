CREATE TABLE evaluation_runs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    knowledge_base_id BIGINT NOT NULL REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    dataset_snapshot JSONB NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'canceled')),
    total_cases INTEGER NOT NULL CHECK (total_cases >= 0),
    finished_cases INTEGER NOT NULL DEFAULT 0
        CHECK (finished_cases >= 0 AND finished_cases <= total_cases),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_token TEXT,
    lease_until TIMESTAMPTZ,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX evaluation_runs_pending_idx
ON evaluation_runs (created_at, id)
WHERE status = 'pending';

CREATE INDEX evaluation_runs_expired_idx
ON evaluation_runs (lease_until, id)
WHERE status = 'running';

CREATE TABLE evaluation_case_results (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    evaluation_run_id BIGINT NOT NULL REFERENCES evaluation_runs (id) ON DELETE CASCADE,
    case_id BIGINT NOT NULL,
    question TEXT NOT NULL,
    reference_answer TEXT,
    generated_answer TEXT,
    retrieved_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    retrieval_metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    generation_metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (evaluation_run_id, case_id)
);

CREATE INDEX evaluation_case_results_run_idx
ON evaluation_case_results (evaluation_run_id, case_id);

