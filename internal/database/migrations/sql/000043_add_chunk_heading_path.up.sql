ALTER TABLE document_parent_chunks
    ADD COLUMN heading_path TEXT NOT NULL DEFAULT '';

ALTER TABLE document_chunks
    ADD COLUMN heading_path TEXT NOT NULL DEFAULT '';
