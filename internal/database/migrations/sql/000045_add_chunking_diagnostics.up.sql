ALTER TABLE documents
    ADD COLUMN chunking_diagnostics JSONB NOT NULL DEFAULT '{}'::jsonb;
