ALTER TABLE conversations
    ADD COLUMN title_search tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED;

ALTER TABLE conversation_messages
    ADD COLUMN content_search tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED;

CREATE INDEX conversations_title_search_gin_idx
    ON conversations USING GIN (title_search);

CREATE INDEX conversations_title_trgm_gin_idx
    ON conversations USING GIN (lower(title) gin_trgm_ops);

CREATE INDEX conversation_messages_content_search_gin_idx
    ON conversation_messages USING GIN (content_search);
