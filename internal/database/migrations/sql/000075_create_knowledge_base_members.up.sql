CREATE TABLE knowledge_base_members (
    knowledge_base_id BIGINT NOT NULL
        REFERENCES knowledge_bases (id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL
        REFERENCES app_users (id) ON DELETE CASCADE,
    role TEXT NOT NULL
        CHECK (role IN ('owner', 'editor', 'viewer')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (knowledge_base_id, user_id)
);

CREATE INDEX knowledge_base_members_user_idx
    ON knowledge_base_members (user_id, knowledge_base_id);

-- Preserve access to legacy local knowledge bases when a user already exists.
-- If app_users is empty, the first registration fills this same gap.
INSERT INTO knowledge_base_members (knowledge_base_id, user_id, role)
SELECT kb.id, first_user.id, 'owner'
FROM knowledge_bases AS kb
CROSS JOIN LATERAL (
    SELECT id FROM app_users ORDER BY id LIMIT 1
) AS first_user
WHERE NOT EXISTS (
    SELECT 1
    FROM knowledge_base_members AS existing
    WHERE existing.knowledge_base_id = kb.id
);
