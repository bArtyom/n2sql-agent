CREATE TABLE knowledge_base_tags (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    knowledge_base_id BIGINT NOT NULL REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(BTRIM(name)) > 0 AND length(name) <= 80),
    color VARCHAR(32) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX knowledge_base_tags_name_unique_idx
    ON knowledge_base_tags (knowledge_base_id, lower(name));

CREATE INDEX knowledge_base_tags_knowledge_base_idx
    ON knowledge_base_tags (knowledge_base_id, lower(name), id);

CREATE TABLE document_tags (
    document_id BIGINT NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    tag_id BIGINT NOT NULL REFERENCES knowledge_base_tags (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (document_id, tag_id)
);

CREATE INDEX document_tags_tag_document_idx ON document_tags (tag_id, document_id);
