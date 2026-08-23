DROP INDEX IF EXISTS document_chunks_image_kind_idx;

ALTER TABLE document_chunks
    DROP COLUMN image_info;

ALTER TABLE document_chunks
    DROP CONSTRAINT IF EXISTS document_chunks_chunk_kind_check;

ALTER TABLE document_chunks
    ADD CONSTRAINT document_chunks_chunk_kind_check
        CHECK (chunk_kind IN ('text', 'summary'));
