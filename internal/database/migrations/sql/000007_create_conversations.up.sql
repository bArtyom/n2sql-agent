CREATE TABLE conversations (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    administrator_id BIGINT NOT NULL REFERENCES administrators (id),
    knowledge_base_id BIGINT NOT NULL REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    title TEXT NOT NULL CHECK (length(BTRIM(title)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE conversation_messages (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content TEXT NOT NULL CHECK (length(BTRIM(content)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX conversations_knowledge_base_updated_idx
ON conversations (knowledge_base_id, updated_at DESC, id DESC);

CREATE INDEX conversation_messages_conversation_id_id_idx
ON conversation_messages (conversation_id, id);

CREATE TRIGGER conversations_refresh_updated_at
BEFORE UPDATE ON conversations
FOR EACH ROW
EXECUTE FUNCTION refresh_updated_at();
