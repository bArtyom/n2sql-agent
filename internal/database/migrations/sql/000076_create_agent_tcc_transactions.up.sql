CREATE TABLE agent_tcc_transactions (
    transaction_id TEXT PRIMARY KEY,
    agent_run_id BIGINT NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    arguments JSONB NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('trying', 'tried', 'confirming', 'confirmed', 'canceling', 'canceled', 'failed')),
    result JSONB,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX agent_tcc_transactions_recovery_idx
ON agent_tcc_transactions (state, updated_at)
WHERE state IN ('trying', 'tried', 'confirming', 'canceling');

CREATE TABLE agent_tcc_branches (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id TEXT NOT NULL REFERENCES agent_tcc_transactions (transaction_id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL,
    participant TEXT NOT NULL,
    arguments JSONB NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('trying', 'tried', 'confirming', 'confirmed', 'canceling', 'canceled', 'failed')),
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (transaction_id, operation_id)
);

CREATE INDEX agent_tcc_branches_recovery_idx
ON agent_tcc_branches (transaction_id, state);
