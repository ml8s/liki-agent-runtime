// Package evidence owns terminal transitions for runtime lifecycle evidence
// records. Functions are pure: callers retain control of append ordering and
// durable persistence.
package evidence

import (
	"fmt"
	"time"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
)

// ToolCallRecord is runtime evidence for one ADK tool invocation.
type ToolCallRecord struct {
	CallID       string
	Tool         string
	RootRunID    domain.ID
	RunID        domain.ID
	ThreadID     domain.ID
	UserID       string
	AgentName    string
	AgentVersion string
	MCPServer    string
	Status       audit.Status
	StartedAt    time.Time
	FinishedAt   time.Time
	DurationMS   int64
	InputDigest  string
	InputBytes   int64
	OutputDigest string
	OutputBytes  int64
	ErrorCode    string
	ErrorMessage string
}

// CompleteToolCall returns the succeeded terminal form of a started tool call.
func CompleteToolCall(record ToolCallRecord, result map[string]any, finishedAt time.Time) (ToolCallRecord, error) {
	if err := requireRunningToolCall(record); err != nil {
		return record, err
	}
	outputDigest, outputBytes, err := audit.CanonicalDigest(result)
	if err != nil {
		return record, domain.NewError(domain.CodeToolProvenanceInvalid, "digest tool result", err)
	}
	next := record
	next.Status = audit.StatusSucceeded
	next.FinishedAt = finishedAt
	next.DurationMS = finishedAt.Sub(record.StartedAt).Milliseconds()
	next.OutputDigest = outputDigest
	next.OutputBytes = outputBytes
	return next, nil
}

// FailToolCall returns the failed terminal form of a started tool call.
func FailToolCall(record ToolCallRecord, finishedAt time.Time, code, message string) (ToolCallRecord, error) {
	if err := requireRunningToolCall(record); err != nil {
		return record, err
	}
	next := record
	next.Status = audit.StatusFailed
	next.FinishedAt = finishedAt
	next.DurationMS = finishedAt.Sub(record.StartedAt).Milliseconds()
	next.ErrorCode = code
	next.ErrorMessage = message
	return next, nil
}

func requireRunningToolCall(record ToolCallRecord) error {
	if record.Status == audit.StatusRunning {
		return nil
	}
	return domain.NewError(domain.CodeToolCallInvalid, fmt.Sprintf("tool call %q is already terminal", record.CallID), nil)
}

// DelegationExecutionRecord is runtime evidence for one sub-agent invocation.
type DelegationExecutionRecord struct {
	DelegationID          string
	InvocationID          string
	CallerAgent           string
	TargetAgent           string
	AgentDefinitionDigest string
	RootRunID             domain.ID
	RunID                 domain.ID
	ThreadID              domain.ID
	UserID                string
	AgentPath             string
	Depth                 int
	Status                audit.Status
	StartedAt             time.Time
	FinishedAt            time.Time
	DurationMS            int64
	ErrorCode             string
	ErrorMessage          string
}

// CompleteDelegation returns the succeeded terminal form of a started delegation.
func CompleteDelegation(record DelegationExecutionRecord, finishedAt time.Time) (DelegationExecutionRecord, error) {
	if err := requireRunningDelegation(record); err != nil {
		return record, err
	}
	next := record
	next.Status = audit.StatusSucceeded
	next.FinishedAt = finishedAt
	next.DurationMS = finishedAt.Sub(record.StartedAt).Milliseconds()
	return next, nil
}

// FailDelegation returns the failed terminal form of a started delegation.
func FailDelegation(record DelegationExecutionRecord, finishedAt time.Time, code, message string) (DelegationExecutionRecord, error) {
	if err := requireRunningDelegation(record); err != nil {
		return record, err
	}
	next := record
	next.Status = audit.StatusFailed
	next.FinishedAt = finishedAt
	next.DurationMS = finishedAt.Sub(record.StartedAt).Milliseconds()
	next.ErrorCode = code
	next.ErrorMessage = message
	return next, nil
}

func requireRunningDelegation(record DelegationExecutionRecord) error {
	if record.Status == audit.StatusRunning {
		return nil
	}
	return domain.NewError(domain.CodeDelegationFailed, fmt.Sprintf("delegation for agent %q is already terminal", record.TargetAgent), nil)
}
