ALTER TABLE knowledge_bases
    ADD COLUMN parser_engine_rules JSONB NOT NULL DEFAULT '[]'::jsonb;
