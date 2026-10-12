-- +goose Up
ALTER TABLE agent_audit_events DROP COLUMN schema_version;

-- +goose Down
ALTER TABLE agent_audit_events
    ADD COLUMN schema_version TEXT NOT NULL DEFAULT '';
