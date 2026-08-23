ALTER TABLE documents
    ADD COLUMN parser_metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE document_assets (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id BIGINT NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    asset_index INTEGER NOT NULL CHECK (asset_index >= 0),
    original_filename TEXT NOT NULL CHECK (length(BTRIM(original_filename)) > 0),
    storage_path TEXT NOT NULL UNIQUE CHECK (
        length(BTRIM(storage_path)) > 0
        AND storage_path !~ '^/'
        AND storage_path !~ '(^|/)\.\.(/|$)'
    ),
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    page INTEGER NOT NULL DEFAULT 0 CHECK (page >= 0),
    source TEXT NOT NULL DEFAULT 'embedded',
    is_original BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (document_id, asset_index)
);

CREATE INDEX document_assets_document_id_idx ON document_assets (document_id, asset_index);
