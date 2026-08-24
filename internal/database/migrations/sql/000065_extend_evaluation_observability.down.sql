DROP INDEX IF EXISTS evaluation_case_results_status_idx;

ALTER TABLE evaluation_case_results
    DROP COLUMN IF EXISTS finished_at,
    DROP COLUMN IF EXISTS started_at,
    DROP COLUMN IF EXISTS estimated_cost_micros,
    DROP COLUMN IF EXISTS total_tokens,
    DROP COLUMN IF EXISTS completion_tokens,
    DROP COLUMN IF EXISTS prompt_tokens,
    DROP COLUMN IF EXISTS duration_ms,
    DROP COLUMN IF EXISTS attempt_count,
    DROP COLUMN IF EXISTS status;

ALTER TABLE evaluation_runs
    DROP COLUMN IF EXISTS estimated_cost_micros,
    DROP COLUMN IF EXISTS total_tokens,
    DROP COLUMN IF EXISTS completion_tokens,
    DROP COLUMN IF EXISTS prompt_tokens,
    DROP COLUMN IF EXISTS duration_ms,
    DROP COLUMN IF EXISTS failed_cases,
    DROP COLUMN IF EXISTS model_config,
    DROP COLUMN IF EXISTS knowledge_base_snapshot,
    DROP COLUMN IF EXISTS dataset_version;
