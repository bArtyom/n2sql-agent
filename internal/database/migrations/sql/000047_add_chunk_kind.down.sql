DROP INDEX IF EXISTS document_chunks_kind_idx;

ALTER TABLE document_chunks
    DROP CONSTRAINT document_chunks_document_position_kind_key;

ALTER TABLE document_chunks
    ADD CONSTRAINT document_chunks_document_id_position_key
        UNIQUE (document_id, position);

ALTER TABLE document_chunks
    DROP COLUMN chunk_kind;
