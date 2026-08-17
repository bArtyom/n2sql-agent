CREATE TABLE conversation_message_feedback (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    administrator_id BIGINT NOT NULL REFERENCES administrators (id) ON DELETE CASCADE,
    conversation_id BIGINT NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    message_id BIGINT NOT NULL REFERENCES conversation_messages (id) ON DELETE CASCADE,
    rating SMALLINT NOT NULL CHECK (rating IN (-1, 1)),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (administrator_id, message_id)
);

CREATE INDEX conversation_message_feedback_conversation_idx
ON conversation_message_feedback (conversation_id, updated_at DESC);
