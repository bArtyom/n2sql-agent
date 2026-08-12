ALTER TABLE a2a_tasks
    ADD COLUMN document_ids BIGINT[] NOT NULL DEFAULT '{}';

ALTER TABLE a2a_tasks
    ADD CONSTRAINT a2a_tasks_document_ids_limit
    CHECK (cardinality(document_ids) <= 100);
