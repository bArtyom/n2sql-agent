DROP INDEX IF EXISTS document_chunks_heading_search_gin_idx;

ALTER TABLE document_chunks
    DROP COLUMN IF EXISTS heading_search;
