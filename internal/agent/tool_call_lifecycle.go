package agent

import (
	"fmt"
	"time"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
)

// succeedToolCall returns the succeeded terminal form of a started tool call.
// The input record is never modified.
func succeedToolCall(
	record ToolCallRecord,
	result map[string]any,
	finishedAt time.Time,
) (ToolCallRecord, error) {
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

// failToolCall returns the failed terminal form of a started tool call.
func failToolCall(
	record ToolCallRecord,
	finishedAt time.Time,
	code string,
	message string,
) (ToolCallRecord, error) {
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
	return domain.NewError(
		domain.CodeToolCallInvalid,
		fmt.Sprintf("tool call %q is already terminal", record.CallID),
		nil,
	)
}
