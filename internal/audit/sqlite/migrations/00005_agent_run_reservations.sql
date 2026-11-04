-- +goose Up
CREATE TABLE agent_audit_run_reservations (
    run_id TEXT PRIMARY KEY,
    reserved_at TIMESTAMP NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER agent_audit_run_reservations_no_update
BEFORE UPDATE ON agent_audit_run_reservations
BEGIN
    SELECT RAISE(ABORT, 'agent run reservations are immutable');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER agent_audit_run_reservations_no_delete
BEFORE DELETE ON agent_audit_run_reservations
BEGIN
    SELECT RAISE(ABORT, 'agent run reservations are immutable');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS agent_audit_run_reservations_no_delete;
DROP TRIGGER IF EXISTS agent_audit_run_reservations_no_update;
DROP TABLE agent_audit_run_reservations;
