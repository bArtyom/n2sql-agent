ALTER TABLE document_processing_tasks
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;

ALTER TABLE document_processing_tasks
    DROP CONSTRAINT document_processing_tasks_status_check;

ALTER TABLE document_processing_tasks
    ADD CONSTRAINT document_processing_tasks_status_check
    CHECK (status IN ('pending', 'processing', 'succeeded', 'failed', 'dead_letter'));

CREATE INDEX document_processing_tasks_retry_idx
    ON document_processing_tasks (next_attempt_at, created_at)
    WHERE status = 'pending';
