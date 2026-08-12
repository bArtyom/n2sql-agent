CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE document_chunks
    ADD COLUMN content_search tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED;

CREATE INDEX document_chunks_content_search_gin_idx
    ON document_chunks USING GIN (content_search);

CREATE INDEX document_chunks_content_trgm_gin_idx
    ON document_chunks USING GIN (lower(content) gin_trgm_ops);

-- HNSW is intentionally deferred. The embedding column is dimension-agnostic
-- so one migration cannot safely choose a fixed expression-index dimension.
-- Once the embedding model dimension is fixed, add a follow-up migration such
-- as: CREATE INDEX ... ON document_chunks USING hnsw
-- ((embedding::vector(1024)) vector_cosine_ops);
