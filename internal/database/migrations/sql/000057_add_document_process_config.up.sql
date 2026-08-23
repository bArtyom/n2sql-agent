ALTER TABLE document_processing_tasks
    ADD COLUMN process_config JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Freeze the knowledge-base parser rules for tasks that were already queued
-- before this migration. Future tasks always write their own snapshot.
UPDATE document_processing_tasks AS task
SET process_config = jsonb_build_object('parser_engine_rules', knowledge_base.parser_engine_rules)
FROM documents AS document
JOIN knowledge_bases AS knowledge_base ON knowledge_base.id = document.knowledge_base_id
WHERE task.document_id = document.id
  AND NOT (task.process_config ? 'parser_engine_rules');
