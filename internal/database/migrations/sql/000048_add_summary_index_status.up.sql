ALTER TABLE documents
    ADD COLUMN summary_index_status TEXT NOT NULL DEFAULT 'none'
        CHECK (summary_index_status IN ('none', 'processing', 'succeeded', 'failed')),
    ADD COLUMN summary_index_error TEXT,
    ADD COLUMN summary_index_updated_at TIMESTAMPTZ;

CREATE INDEX documents_summary_index_status_idx
    ON documents (summary_index_status);
