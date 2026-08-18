CREATE TABLE user_memory_profiles (
    user_id BIGINT PRIMARY KEY REFERENCES app_users (id) ON DELETE CASCADE,
    content TEXT NOT NULL DEFAULT '',
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (length(content) <= 6000)
);
