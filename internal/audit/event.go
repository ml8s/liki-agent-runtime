// Package audit defines the durable execution evidence format used by the
// Agent runtime. Audit events are immutable facts; they are not telemetry and
// must never contain user prompts, model raw output, secrets, or tool payloads.
package audit

import (
	"fmt"
	"strings"
	"time"

	"github.com/ml8s/agent-runtime/internal/domain"
)

// Audit event type identifiers.
const (
	EventRunStarted          EventType = "run.started"
	EventRunCompleted        EventType = "run.completed"
	EventRunFailed           EventType = "run.failed"
	EventRunPaused           EventType = "run.paused"
	EventRunCanceled         EventType = "run.canceled"
	EventLLMCallStarted      EventType = "llm.call.started"
	EventLLMCallCompleted    EventType = "llm.call.completed"
	EventLLMCallFailed       EventType = "llm.call.failed"
	EventToolCallStarted     EventType = "tool.call.started"
	EventToolCallCompleted   EventType = "tool.call.completed"
	EventToolCallFailed      EventType = "tool.call.failed"
	EventDelegationStarted   EventType = "agent.delegation.started"
	EventDelegationCompleted EventType = "agent.delegation.completed"
	EventDelegationFailed    EventType = "agent.delegation.failed"
)

// EventType identifies a lifecycle fact. Event names are stable identifiers.
type EventType string

// Status is the outcome represented by a lifecycle event.
type Status string

// Audit event statuses.
const (
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusPaused    Status = "paused"
	StatusCanceled  Status = "canceled"
)

// Event is one immutable execution fact.
type Event struct {
	ID                    string
	Type                  EventType
	OccurredAt            time.Time
	RootRunID             domain.ID
	RunID                 domain.ID
	ParentRunID           domain.ID
	ThreadID              domain.ID
	UserID                string
	Protocol              string
	TraceID               string
	SpanID                string
	AgentName             string
	DefinitionName        string
	DefinitionVersion     string
	DefinitionDigest      string
	AgentVersion          string
	AgentDefinitionDigest string
	CallerAgent           string
	TargetAgent           string
	DelegationDepth       int
	ToolCallID            string
	ToolName              string
	Model                 string
	Provider              string
	Status                Status
	DurationMS            int64
	ErrorCode             string
	ErrorMessage          string
	Payload               map[string]any
}

// ValidateEvent normalizes an event and reports whether it carries the
// required lifecycle evidence. The normalized value must replace the input;
// validation never mutates the caller's event.
func ValidateEvent(event Event) (Event, error) {
	event = normalizeEvent(event)
	if err := event.validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// normalizeEvent returns an equivalent event with whitespace removed from
// identifier and provenance fields. The input event is never modified.
func normalizeEvent(event Event) Event {
	event.ID = strings.TrimSpace(event.ID)
	event.Type = EventType(strings.TrimSpace(string(event.Type)))
	event.UserID = strings.TrimSpace(event.UserID)
	event.Protocol = strings.TrimSpace(event.Protocol)
	event.TraceID = strings.ToLower(strings.TrimSpace(event.TraceID))
	event.SpanID = strings.ToLower(strings.TrimSpace(event.SpanID))
	event.AgentName = strings.TrimSpace(event.AgentName)
	event.AgentVersion = strings.TrimSpace(event.AgentVersion)
	event.CallerAgent = strings.TrimSpace(event.CallerAgent)
	event.TargetAgent = strings.TrimSpace(event.TargetAgent)
	event.ToolName = strings.TrimSpace(event.ToolName)
	event.Model = strings.TrimSpace(event.Model)
	event.Provider = strings.TrimSpace(event.Provider)
	event.ErrorCode = strings.TrimSpace(event.ErrorCode)
	return event
}

// validate reports whether the already-normalized event carries the required
// lifecycle evidence.
func (e Event) validate() error {
	switch {
	case e.ID == "":
		return NewError(CodeIDRequired, "audit event id is required", domain.ErrInvalidInput)
	case e.Type == "":
		return NewError(CodeTypeRequired, "audit event type is required", domain.ErrInvalidInput)
	case !knownEventType(e.Type):
		return NewError(CodeTypeInvalid, fmt.Sprintf("unknown audit event type %q", e.Type), domain.ErrInvalidInput)
	case e.OccurredAt.IsZero():
		return NewError(CodeOccurredAtRequired, "audit event occurred_at is required", domain.ErrInvalidInput)
	case e.RunID == "":
		return NewError(CodeRunIDRequired, "audit event run id is required", domain.ErrInvalidInput)
	case e.RootRunID == "":
		return NewError(CodeRootRunIDRequired, "audit event root run id is required", domain.ErrInvalidInput)
	}

	if e.DelegationDepth < 0 {
		return NewError(CodeInvalidDelegationDepth, "audit event delegation depth must not be negative", domain.ErrInvalidInput)
	}

	var validStatus bool
	switch e.Status {
	case "", StatusRunning, StatusSucceeded, StatusFailed, StatusPaused, StatusCanceled:
		validStatus = true
	}
	if !validStatus {
		return NewError(CodeStatusInvalid, "invalid audit event status", domain.ErrInvalidInput)
	}

	if e.DurationMS < 0 {
		return NewError(CodeInvalidDuration, "audit event duration must not be negative", domain.ErrInvalidInput)
	}
	if err := validateTraceIDs(e.TraceID, e.SpanID); err != nil {
		return err
	}

	switch e.Type {
	case EventRunStarted, EventLLMCallStarted, EventToolCallStarted, EventDelegationStarted:
		if e.Status != StatusRunning {
			return NewError(CodeStatusInvalid, "started audit event must be running")
		}
	case EventRunCompleted, EventLLMCallCompleted, EventToolCallCompleted, EventDelegationCompleted:
		if e.Status != StatusSucceeded {
			return NewError(CodeStatusInvalid, "completed audit event must be succeeded")
		}
	case EventRunPaused:
		if e.Status != StatusPaused {
			return NewError(CodeStatusInvalid, "paused audit event must be paused")
		}
	case EventRunCanceled:
		if e.Status != StatusCanceled {
			return NewError(CodeStatusInvalid, "paused audit event must be paused")
		}
	case EventRunFailed, EventLLMCallFailed, EventToolCallFailed, EventDelegationFailed:
		if e.Status != StatusFailed {
			return NewError(CodeStatusInvalid, "failed audit event must be failed")
		}
		if e.ErrorCode == "" {
			return NewError(CodeErrorCodeRequired, "failed audit event requires an error code", domain.ErrInvalidInput)
		}
	}

	switch e.Type {
	case EventLLMCallStarted, EventLLMCallCompleted, EventLLMCallFailed:
		if e.AgentName == "" || e.Model == "" {
			return NewError(CodeLLMProvenanceRequired, "LLM audit event requires agent and model provenance", domain.ErrInvalidInput)
		}
	case EventDelegationStarted, EventDelegationCompleted, EventDelegationFailed:
		if e.CallerAgent == "" || e.TargetAgent == "" {
			return NewError(CodeDelegationProvenanceRequired, "delegation audit event requires caller and target agents", domain.ErrInvalidInput)
		}
	case EventToolCallStarted, EventToolCallCompleted, EventToolCallFailed:
		if e.ToolCallID == "" {
			return NewError(CodeToolEventCallIDRequired, "tool audit event call id is required", domain.ErrInvalidInput)
		}
		if e.ToolName == "" {
			return NewError(CodeToolEventNameRequired, "tool audit event tool name is required", domain.ErrInvalidInput)
		}
	}
	return nil
}

func knownEventType(eventType EventType) bool {
	switch eventType {
	case EventRunStarted, EventRunCompleted, EventRunFailed, EventRunPaused, EventRunCanceled,
		EventLLMCallStarted, EventLLMCallCompleted, EventLLMCallFailed,
		EventToolCallStarted, EventToolCallCompleted, EventToolCallFailed,
		EventDelegationStarted, EventDelegationCompleted, EventDelegationFailed:
		return true
	default:
		return false
	}
}

func validateTraceIDs(traceID string, spanID string) error {
	if traceID == "" && spanID == "" {
		return nil
	}
	if len(traceID) != 32 || strings.ToLower(traceID) != traceID || !isHex(traceID) {
		return NewError(CodeTraceIDInvalid, "audit event trace id must be 32 lowercase hex characters", domain.ErrInvalidInput)
	}
	if len(spanID) != 16 || strings.ToLower(spanID) != spanID || !isHex(spanID) {
		return NewError(CodeSpanIDInvalid, "audit event span id must be 16 lowercase hex characters", domain.ErrInvalidInput)
	}
	return nil
}

func isHex(value string) bool {
	for _, char := range value {
		switch char {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f':
		default:
			return false
		}
	}
	return true
}

// NewError builds a coded audit error consistent with the domain error type.
func NewError(code string, message string, cause ...error) error {
	var wrapped error
	if len(cause) > 0 {
		wrapped = cause[0]
	}
	return domain.NewError(code, message, wrapped)
}

// Common audit error codes. They live beside the event format so all audit
// adapters return the same stable identifiers.
const (
	CodeIDRequired                   = "audit_event_id_required"
	CodeTypeRequired                 = "audit_event_type_required"
	CodeTypeInvalid                  = "audit_event_type_invalid"
	CodeErrorCodeRequired            = "audit_event_error_code_required"
	CodeLLMProvenanceRequired        = "audit_event_llm_provenance_required"
	CodeDelegationProvenanceRequired = "audit_event_delegation_provenance_required"
	CodeOccurredAtRequired           = "audit_event_occurred_at_required"
	CodeRunIDRequired                = "audit_event_run_id_required"
	CodeRootRunIDRequired            = "audit_event_root_run_id_required"
	CodeInvalidDelegationDepth       = "audit_event_delegation_depth_invalid"
	CodeStatusInvalid                = "audit_event_status_invalid"
	CodeInvalidDuration              = "audit_event_duration_invalid"
	CodeTraceIDInvalid               = "audit_event_trace_id_invalid"
	CodeSpanIDInvalid                = "audit_event_span_id_invalid"
	CodeToolEventCallIDRequired      = "audit_event_tool_call_id_required"
	CodeToolEventNameRequired        = "audit_event_tool_name_required"
	CodeAuditAppendFailed            = "audit_append_failed"
	CodeAuditEventConflict           = "audit_event_conflict"
)

// StatusFor maps a lifecycle event type to its expected audit status.
// Terminal event types map to succeeded or failed; every other type
// (including run.started, llm.call.started, and tool.call.started) is running.
func StatusFor(eventType EventType) Status {
	switch eventType {
	case EventRunCompleted, EventLLMCallCompleted,
		EventToolCallCompleted, EventDelegationCompleted:
		return StatusSucceeded
	case EventRunFailed, EventLLMCallFailed,
		EventToolCallFailed, EventDelegationFailed:
		return StatusFailed
	case EventRunPaused:
		return StatusPaused
	case EventRunCanceled:
		return StatusCanceled
	default:
		return StatusRunning
	}
}
