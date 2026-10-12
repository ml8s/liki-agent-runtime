package sqlite

import (
	"encoding/json"
	"time"

	"github.com/ml8s/liki-agents/internal/audit"
)

type auditEventModel struct {
	ID                    string    `gorm:"column:id;primaryKey"`
	EventType             string    `gorm:"column:event_type;index"`
	OccurredAt            time.Time `gorm:"column:occurred_at;index"`
	RootRunID             string    `gorm:"column:root_run_id;index"`
	RunID                 string    `gorm:"column:run_id;index"`
	ParentRunID           string    `gorm:"column:parent_run_id;index"`
	ThreadID              string    `gorm:"column:thread_id;index"`
	UserID                string    `gorm:"column:user_id"`
	Protocol              string    `gorm:"column:protocol"`
	TraceID               string    `gorm:"column:trace_id"`
	SpanID                string    `gorm:"column:span_id"`
	AgentName             string    `gorm:"column:agent_name;index"`
	DefinitionName        string    `gorm:"column:definition_name;index"`
	DefinitionVersion     string    `gorm:"column:definition_version"`
	DefinitionDigest      string    `gorm:"column:definition_digest"`
	AgentVersion          string    `gorm:"column:agent_version"`
	AgentDefinitionDigest string    `gorm:"column:agent_definition_digest"`
	CallerAgent           string    `gorm:"column:caller_agent"`
	TargetAgent           string    `gorm:"column:target_agent"`
	DelegationDepth       int       `gorm:"column:delegation_depth"`
	ToolCallID            string    `gorm:"column:tool_call_id;index"`
	ToolName              string    `gorm:"column:tool_name;index"`
	Model                 string    `gorm:"column:model;index"`
	Provider              string    `gorm:"column:provider;index"`
	Status                string    `gorm:"column:status"`
	DurationMS            int64     `gorm:"column:duration_ms"`
	ErrorCode             string    `gorm:"column:error_code"`
	ErrorMessage          string    `gorm:"column:error_message"`
	PayloadJSON           string    `gorm:"column:payload_json"`
}

func (auditEventModel) TableName() string { return "agent_audit_events" }

func newAuditEventModel(event *audit.Event) (auditEventModel, error) {
	payload := "{}"
	if len(event.Payload) != 0 {
		raw, err := json.Marshal(event.Payload)
		if err != nil {
			return auditEventModel{}, audit.NewError(audit.CodeAuditAppendFailed, "marshal audit payload", err)
		}
		payload = string(raw)
	}
	return auditEventModel{
		ID:                    event.ID,
		EventType:             string(event.Type),
		OccurredAt:            event.OccurredAt,
		RootRunID:             string(event.RootRunID),
		RunID:                 string(event.RunID),
		ParentRunID:           string(event.ParentRunID),
		ThreadID:              string(event.ThreadID),
		UserID:                event.UserID,
		Protocol:              event.Protocol,
		TraceID:               event.TraceID,
		SpanID:                event.SpanID,
		AgentName:             event.AgentName,
		DefinitionName:        event.DefinitionName,
		DefinitionVersion:     event.DefinitionVersion,
		DefinitionDigest:      event.DefinitionDigest,
		AgentVersion:          event.AgentVersion,
		AgentDefinitionDigest: event.AgentDefinitionDigest,
		CallerAgent:           event.CallerAgent,
		TargetAgent:           event.TargetAgent,
		DelegationDepth:       event.DelegationDepth,
		ToolCallID:            event.ToolCallID,
		ToolName:              event.ToolName,
		Model:                 event.Model,
		Provider:              event.Provider,
		Status:                string(event.Status),
		DurationMS:            event.DurationMS,
		ErrorCode:             event.ErrorCode,
		ErrorMessage:          event.ErrorMessage,
		PayloadJSON:           payload,
	}, nil
}
