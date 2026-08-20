ALTER TABLE agent_runs
    DROP CONSTRAINT agent_runs_failure_category_check;

ALTER TABLE agent_runs
    DROP COLUMN failure_category;
