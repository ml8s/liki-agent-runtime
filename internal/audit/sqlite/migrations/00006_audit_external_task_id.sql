-- +goose Up
ALTER TABLE agent_audit_events
    ADD COLUMN task_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_agent_audit_events_task_id
    ON agent_audit_events(task_id);

-- +goose Down
DROP INDEX IF EXISTS idx_agent_audit_events_task_id;
ALTER TABLE agent_audit_events DROP COLUMN task_id;
