ALTER TABLE documents
    ADD COLUMN summary TEXT NOT NULL DEFAULT '',
    ADD COLUMN summary_status TEXT NOT NULL DEFAULT 'none'
        CHECK (summary_status IN ('none', 'processing', 'succeeded', 'failed')),
    ADD COLUMN summary_error TEXT,
    ADD COLUMN summary_updated_at TIMESTAMPTZ;

CREATE INDEX documents_summary_status_idx ON documents (summary_status);
