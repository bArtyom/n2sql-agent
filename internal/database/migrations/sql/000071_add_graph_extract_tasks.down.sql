DROP INDEX IF EXISTS document_postprocess_tasks_graph_chunk_idx;

ALTER TABLE document_postprocess_tasks
    DROP CONSTRAINT IF EXISTS document_postprocess_tasks_task_kind_check;

ALTER TABLE document_postprocess_tasks
    ADD CONSTRAINT document_postprocess_tasks_task_kind_check CHECK (task_kind IN (
        'document_summary', 'summary_index', 'image_ocr', 'image_caption',
        'follow_up', 'faq', 'wiki', 'recommended_query'
    ));

ALTER TABLE document_postprocess_tasks
    DROP COLUMN IF EXISTS chunk_position;
