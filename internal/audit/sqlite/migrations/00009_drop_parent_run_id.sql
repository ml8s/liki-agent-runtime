-- +goose Up
ALTER TABLE agent_audit_events
    DROP COLUMN parent_run_id;

-- +goose Down
ALTER TABLE agent_audit_events
    ADD COLUMN parent_run_id TEXT NOT NULL DEFAULT '';
