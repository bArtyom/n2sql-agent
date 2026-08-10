CREATE TABLE conversation_idempotency_keys (
    conversation_id BIGINT NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL CHECK (length(BTRIM(idempotency_key)) BETWEEN 1 AND 128),
    request_hash TEXT NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (conversation_id, idempotency_key)
);
