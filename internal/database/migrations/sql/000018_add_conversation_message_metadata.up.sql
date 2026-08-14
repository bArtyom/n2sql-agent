ALTER TABLE conversation_messages
    ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE conversation_messages
    ADD CONSTRAINT conversation_messages_metadata_object_check
    CHECK (jsonb_typeof(metadata) = 'object');
