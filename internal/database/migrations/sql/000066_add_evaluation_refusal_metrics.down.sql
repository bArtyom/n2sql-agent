ALTER TABLE evaluation_case_results
    DROP COLUMN IF EXISTS correct_refusal,
    DROP COLUMN IF EXISTS refused,
    DROP COLUMN IF EXISTS expected_relevant;

ALTER TABLE evaluation_runs
    DROP COLUMN IF EXISTS unsupported_accepts,
    DROP COLUMN IF EXISTS false_refusals,
    DROP COLUMN IF EXISTS correct_refusals,
    DROP COLUMN IF EXISTS expected_irrelevant_cases,
    DROP COLUMN IF EXISTS expected_relevant_cases;
