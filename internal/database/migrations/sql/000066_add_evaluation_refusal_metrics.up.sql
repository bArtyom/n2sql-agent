ALTER TABLE evaluation_runs
    ADD COLUMN expected_relevant_cases INTEGER NOT NULL DEFAULT 0 CHECK (expected_relevant_cases >= 0),
    ADD COLUMN expected_irrelevant_cases INTEGER NOT NULL DEFAULT 0 CHECK (expected_irrelevant_cases >= 0),
    ADD COLUMN correct_refusals INTEGER NOT NULL DEFAULT 0 CHECK (correct_refusals >= 0),
    ADD COLUMN false_refusals INTEGER NOT NULL DEFAULT 0 CHECK (false_refusals >= 0),
    ADD COLUMN unsupported_accepts INTEGER NOT NULL DEFAULT 0 CHECK (unsupported_accepts >= 0);

ALTER TABLE evaluation_case_results
    ADD COLUMN expected_relevant BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN refused BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN correct_refusal BOOLEAN NOT NULL DEFAULT FALSE;
