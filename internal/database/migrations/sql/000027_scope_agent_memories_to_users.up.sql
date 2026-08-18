ALTER TABLE agent_memories
    ADD COLUMN user_id BIGINT REFERENCES app_users (id) ON DELETE CASCADE;

DROP INDEX agent_memories_knowledge_base_content_idx;

CREATE UNIQUE INDEX agent_memories_user_content_idx
ON agent_memories (user_id, knowledge_base_id, lower(content))
WHERE user_id IS NOT NULL;

CREATE INDEX agent_memories_user_knowledge_base_updated_idx
ON agent_memories (user_id, knowledge_base_id, updated_at DESC, id DESC)
WHERE user_id IS NOT NULL;
