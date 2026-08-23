ALTER TABLE documents
    ADD COLUMN folder_path VARCHAR(1024) NOT NULL DEFAULT '';

CREATE INDEX documents_knowledge_base_folder_idx
    ON documents (knowledge_base_id, folder_path, created_at DESC, id DESC);
