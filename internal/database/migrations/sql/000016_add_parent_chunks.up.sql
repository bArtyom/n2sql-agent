CREATE TABLE document_parent_chunks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id BIGINT NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    content TEXT NOT NULL CHECK (length(BTRIM(content)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (document_id, position)
);

ALTER TABLE document_chunks
    ADD COLUMN parent_chunk_id BIGINT REFERENCES document_parent_chunks (id) ON DELETE CASCADE;

CREATE INDEX document_parent_chunks_document_id_position_idx
    ON document_parent_chunks (document_id, position);

CREATE INDEX document_chunks_parent_chunk_id_idx
    ON document_chunks (parent_chunk_id);
