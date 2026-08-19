DELETE FROM agent_run_checkpoints
WHERE NULLIF(payload ->> 'arguments', '') IS NULL
   OR NULLIF(payload ->> 'decision_id', '') IS NULL;
