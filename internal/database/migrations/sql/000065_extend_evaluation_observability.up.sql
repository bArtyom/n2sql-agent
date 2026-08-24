ALTER TABLE evaluation_runs
    ADD COLUMN dataset_version TEXT NOT NULL DEFAULT '',
    ADD COLUMN knowledge_base_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN model_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN failed_cases INTEGER NOT NULL DEFAULT 0 CHECK (failed_cases >= 0),
    ADD COLUMN duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    ADD COLUMN completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    ADD COLUMN total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
    ADD COLUMN estimated_cost_micros BIGINT NOT NULL DEFAULT 0 CHECK (estimated_cost_micros >= 0);

ALTER TABLE evaluation_case_results
    ADD COLUMN status TEXT NOT NULL DEFAULT 'succeeded'
        CHECK (status IN ('succeeded', 'failed')),
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 1 CHECK (attempt_count > 0),
    ADD COLUMN duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    ADD COLUMN completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    ADD COLUMN total_tokens INTEGER NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
    ADD COLUMN estimated_cost_micros BIGINT NOT NULL DEFAULT 0 CHECK (estimated_cost_micros >= 0),
    ADD COLUMN started_at TIMESTAMPTZ,
    ADD COLUMN finished_at TIMESTAMPTZ;

CREATE INDEX evaluation_case_results_status_idx
    ON evaluation_case_results (evaluation_run_id, status, case_id);
