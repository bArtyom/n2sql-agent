DROP INDEX IF EXISTS documents_knowledge_base_content_sha256_idx;
ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_content_sha256_format_check;
ALTER TABLE documents DROP COLUMN IF EXISTS content_sha256;
