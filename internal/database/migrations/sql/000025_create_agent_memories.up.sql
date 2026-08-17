CREATE TABLE agent_memories (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    knowledge_base_id BIGINT NOT NULL REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    content TEXT NOT NULL CHECK (length(BTRIM(content)) > 0 AND length(content) <= 2000),
    source TEXT NOT NULL DEFAULT 'explicit' CHECK (source IN ('explicit', 'derived')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX agent_memories_knowledge_base_content_idx
ON agent_memories (knowledge_base_id, lower(content));

CREATE INDEX agent_memories_knowledge_base_updated_idx
ON agent_memories (knowledge_base_id, updated_at DESC, id DESC);
