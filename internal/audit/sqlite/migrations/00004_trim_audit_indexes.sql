-- +goose Up
-- Write-path trim: the audit ledger is write-heavy and read almost only by
-- incident investigation. Keep the indexes the runtime itself needs —
-- run_id (idempotency probes), event_type+occurred_at (recovery scan), and
-- trace_id (incident correlation) — and drop dimensions whose aggregate
-- questions are answered by Prometheus counters instead. Dropped dimensions
-- stay queryable, only via a table scan.
DROP INDEX IF EXISTS idx_agent_audit_events_root_run_id;
DROP INDEX IF EXISTS idx_agent_audit_events_definition;
DROP INDEX IF EXISTS idx_agent_audit_events_agent_occurred;
DROP INDEX IF EXISTS idx_agent_audit_events_tool_call_id;
DROP INDEX IF EXISTS idx_agent_audit_events_tool_occurred;

-- +goose Down
CREATE INDEX idx_agent_audit_events_root_run_id ON agent_audit_events(root_run_id);
CREATE INDEX idx_agent_audit_events_definition ON agent_audit_events(definition_name, definition_version);
CREATE INDEX idx_agent_audit_events_agent_occurred ON agent_audit_events(agent_name, occurred_at);
CREATE INDEX idx_agent_audit_events_tool_call_id ON agent_audit_events(tool_call_id);
CREATE INDEX idx_agent_audit_events_tool_occurred ON agent_audit_events(tool_name, occurred_at);
