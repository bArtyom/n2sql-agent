ALTER TABLE document_chunks
    ADD COLUMN chunk_kind TEXT NOT NULL DEFAULT 'text'
        CHECK (chunk_kind IN ('text', 'summary'));

ALTER TABLE document_chunks
    DROP CONSTRAINT document_chunks_document_id_position_key;

ALTER TABLE document_chunks
    ADD CONSTRAINT document_chunks_document_position_kind_key
        UNIQUE (document_id, position, chunk_kind);

CREATE INDEX document_chunks_kind_idx
    ON document_chunks (document_id, chunk_kind, position);
