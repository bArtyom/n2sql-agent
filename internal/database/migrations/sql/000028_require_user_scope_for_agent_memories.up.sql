DELETE FROM agent_memories
WHERE user_id IS NULL;

ALTER TABLE agent_memories
    ALTER COLUMN user_id SET NOT NULL;
