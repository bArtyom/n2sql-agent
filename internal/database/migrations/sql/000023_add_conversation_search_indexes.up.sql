ALTER TABLE conversations
    ADD COLUMN title_search tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED;

CREATE INDEX conversations_title_search_gin_idx
    ON conversations USING GIN (title_search);

CREATE INDEX conversations_title_trgm_gin_idx
    ON conversations USING GIN (lower(title) gin_trgm_ops);
