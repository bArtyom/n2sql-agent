DROP INDEX IF EXISTS agent_runs_lease_token_idx;

ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS lease_token;
