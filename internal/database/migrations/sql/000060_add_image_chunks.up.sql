ALTER TABLE document_chunks
    DROP CONSTRAINT IF EXISTS document_chunks_chunk_kind_check;

ALTER TABLE document_chunks
    ADD CONSTRAINT document_chunks_chunk_kind_check
        CHECK (chunk_kind IN ('text', 'summary', 'image_ocr', 'image_caption'));

ALTER TABLE document_chunks
    ADD COLUMN image_info JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX document_chunks_image_kind_idx
    ON document_chunks (document_id, chunk_kind, position)
    WHERE chunk_kind IN ('image_ocr', 'image_caption');
