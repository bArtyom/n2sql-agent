DROP INDEX IF EXISTS documents_summary_index_status_idx;

ALTER TABLE documents
    DROP COLUMN summary_index_updated_at,
    DROP COLUMN summary_index_error,
    DROP COLUMN summary_index_status;
