ALTER TABLE document_postprocess_tasks
    ADD COLUMN chunk_position INTEGER CHECK (chunk_position IS NULL OR chunk_position >= 0);

ALTER TABLE document_postprocess_tasks
    DROP CONSTRAINT IF EXISTS document_postprocess_tasks_task_kind_check;

ALTER TABLE document_postprocess_tasks
    ADD CONSTRAINT document_postprocess_tasks_task_kind_check CHECK (task_kind IN (
        'document_summary', 'summary_index', 'image_ocr', 'image_caption',
        'follow_up', 'faq', 'wiki', 'recommended_query', 'graph_extract'
    ));

CREATE INDEX document_postprocess_tasks_graph_chunk_idx
    ON document_postprocess_tasks (document_id, chunk_position)
    WHERE task_kind = 'graph_extract';
