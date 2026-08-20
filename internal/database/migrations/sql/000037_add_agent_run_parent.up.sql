ALTER TABLE agent_runs
    ADD COLUMN parent_run_id BIGINT REFERENCES agent_runs (id) ON DELETE CASCADE,
    ADD COLUMN run_kind TEXT NOT NULL DEFAULT 'root'
        CHECK (run_kind IN ('root', 'child'));

CREATE INDEX agent_runs_parent_idx
    ON agent_runs (parent_run_id, created_at, id)
    WHERE parent_run_id IS NOT NULL;
