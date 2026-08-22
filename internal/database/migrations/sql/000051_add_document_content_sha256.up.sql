ALTER TABLE documents
    ADD COLUMN content_sha256 TEXT;

ALTER TABLE documents
    ADD CONSTRAINT documents_content_sha256_format_check
    CHECK (content_sha256 IS NULL OR content_sha256 ~ '^[0-9a-f]{64}$');

CREATE UNIQUE INDEX documents_knowledge_base_content_sha256_idx
ON documents (knowledge_base_id, content_sha256)
WHERE content_sha256 IS NOT NULL;
