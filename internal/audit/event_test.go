package audit

import (
	"errors"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/domain"
)

func TestEventValidate(t *testing.T) {
	valid := Event{
		ID:   "run_1:started",
		Type: EventRunStarted, OccurredAt: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		RootRunID: "run_1", RunID: "run_1", ThreadID: "thread_1",
		UserID: "user_1", AgentName: "coordinator", Status: StatusRunning,
	}
	if _, err := ValidateEvent(valid); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	testCases := []struct {
		name     string
		mutate   func(*Event)
		wantCode string
	}{
		{name: "missing id", mutate: func(e *Event) { e.ID = "" }, wantCode: CodeIDRequired},
		{name: "missing type", mutate: func(e *Event) { e.Type = "" }, wantCode: CodeTypeRequired},
		{name: "unknown type", mutate: func(e *Event) { e.Type = "run.deleted" }, wantCode: CodeTypeInvalid},
		{name: "missing occurred at", mutate: func(e *Event) { e.OccurredAt = time.Time{} }, wantCode: CodeOccurredAtRequired},
		{name: "missing run id", mutate: func(e *Event) { e.RunID = "" }, wantCode: CodeRunIDRequired},
		{name: "missing root run id", mutate: func(e *Event) { e.RootRunID = "" }, wantCode: CodeRootRunIDRequired},
		{name: "negative delegation depth", mutate: func(e *Event) { e.DelegationDepth = -1 }, wantCode: CodeInvalidDelegationDepth},
		{name: "invalid status", mutate: func(e *Event) { e.Status = Status("unknown") }, wantCode: CodeStatusInvalid},
		{name: "negative duration", mutate: func(e *Event) { e.DurationMS = -1 }, wantCode: CodeInvalidDuration},
		{name: "invalid trace id", mutate: func(e *Event) { e.TraceID = "bad"; e.SpanID = "0123456789abcdef" }, wantCode: CodeTraceIDInvalid},
		{name: "invalid span id", mutate: func(e *Event) { e.TraceID = "0102030405060708090a0b0c0d0e0f10"; e.SpanID = "bad" }, wantCode: CodeSpanIDInvalid},
		{
			name: "tool event missing call id",
			mutate: func(e *Event) {
				e.Type = EventToolCallCompleted
				e.ToolCallID = ""
				e.ToolName = "tool_a"
				e.Status = StatusSucceeded
			},
			wantCode: CodeToolEventCallIDRequired,
		},
		{
			name: "tool event missing tool name",
			mutate: func(e *Event) {
				e.Type = EventToolCallCompleted
				e.ToolCallID = "call_1"
				e.ToolName = ""
				e.Status = StatusSucceeded
			},
			wantCode: CodeToolEventNameRequired,
		},
		{
			name: "failed tool event missing error code",
			mutate: func(e *Event) {
				e.Type = EventToolCallFailed
				e.ToolCallID = "call_1"
				e.ToolName = "tool_a"
				e.ErrorCode = ""
				e.Status = StatusFailed
			},
			wantCode: CodeErrorCodeRequired,
		},
		{
			name: "llm event missing provenance",
			mutate: func(e *Event) {
				e.Type = EventLLMCallCompleted
				e.Status = StatusSucceeded
				e.AgentName = ""
				e.Model = ""
			},
			wantCode: CodeLLMProvenanceRequired,
		},
		{
			name: "delegation event missing provenance",
			mutate: func(e *Event) {
				e.Type = EventDelegationStarted
				e.Status = StatusRunning
				e.CallerAgent = ""
				e.TargetAgent = ""
			},
			wantCode: CodeDelegationProvenanceRequired,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			event := valid
			testCase.mutate(&event)
			_, err := ValidateEvent(event)
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != testCase.wantCode {
				t.Fatalf("Validate() error = %v, want code %s", err, testCase.wantCode)
			}
		})
	}
}
