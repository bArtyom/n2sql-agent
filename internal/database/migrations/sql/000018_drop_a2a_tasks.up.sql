-- The product now exposes one standard Agent conversation mode. The old A2A
-- task queue is no longer used and can be removed after upgrading.
DROP TABLE IF EXISTS a2a_tasks;
