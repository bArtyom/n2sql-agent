CREATE TABLE conversation_summaries (
    conversation_id BIGINT PRIMARY KEY REFERENCES conversations (id) ON DELETE CASCADE,
    through_message_id BIGINT NOT NULL,
    content TEXT NOT NULL CHECK (length(BTRIM(content)) > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX conversation_summaries_through_message_idx
ON conversation_summaries (conversation_id, through_message_id);
