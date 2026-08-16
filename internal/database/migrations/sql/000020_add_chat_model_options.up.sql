ALTER TABLE model_providers
    ADD COLUMN chat_models TEXT[] NOT NULL DEFAULT '{}';

UPDATE model_providers
SET chat_models = ARRAY[chat_model]
WHERE cardinality(chat_models) = 0;
