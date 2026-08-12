ALTER TABLE model_providers
    ADD COLUMN rerank_base_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN rerank_model TEXT NOT NULL DEFAULT '';
