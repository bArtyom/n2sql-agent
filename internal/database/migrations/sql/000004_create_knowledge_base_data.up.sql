CREATE TABLE knowledge_bases (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    administrator_id BIGINT NOT NULL REFERENCES administrators (id),
    name TEXT NOT NULL CHECK (length(BTRIM(name)) > 0),
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (administrator_id, name)
);

CREATE TABLE documents (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    knowledge_base_id BIGINT NOT NULL REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    original_filename TEXT NOT NULL CHECK (length(BTRIM(original_filename)) > 0),
    storage_path TEXT NOT NULL UNIQUE CHECK (
        length(BTRIM(storage_path)) > 0
        AND storage_path !~ '^/'
        AND storage_path !~ '(^|/)\\.\\.(/|$)'
    ),
    content_type TEXT NOT NULL CHECK (content_type IN ('text/markdown', 'text/plain', 'application/pdf')),
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE document_processing_tasks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id BIGINT NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'succeeded', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX documents_knowledge_base_id_idx ON documents (knowledge_base_id);
CREATE UNIQUE INDEX document_processing_tasks_active_document_idx ON document_processing_tasks (document_id)
WHERE status IN ('pending', 'processing');
CREATE INDEX document_processing_tasks_pending_idx ON document_processing_tasks (created_at)
WHERE status = 'pending';

CREATE TRIGGER knowledge_bases_refresh_updated_at
BEFORE UPDATE ON knowledge_bases
FOR EACH ROW
EXECUTE FUNCTION refresh_updated_at();

CREATE TRIGGER documents_refresh_updated_at
BEFORE UPDATE ON documents
FOR EACH ROW
EXECUTE FUNCTION refresh_updated_at();

CREATE TRIGGER document_processing_tasks_refresh_updated_at
BEFORE UPDATE ON document_processing_tasks
FOR EACH ROW
EXECUTE FUNCTION refresh_updated_at();
