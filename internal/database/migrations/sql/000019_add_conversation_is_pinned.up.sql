ALTER TABLE conversations
ADD COLUMN is_pinned BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX conversations_knowledge_base_pinned_updated_idx
ON conversations (knowledge_base_id, is_pinned DESC, updated_at DESC, id DESC);
