-- +goose Up
-- Startup recovery owns only states that represent in-process execution. A
-- durable ContextID session may legitimately wait in input-required or
-- auth-required across a restart.
CREATE INDEX idx_a2a_tasks_startup_recovery
    ON a2a_tasks(task_id)
    WHERE state IN ('', 'TASK_STATE_SUBMITTED', 'TASK_STATE_WORKING');

-- +goose Down
DROP INDEX IF EXISTS idx_a2a_tasks_startup_recovery;
