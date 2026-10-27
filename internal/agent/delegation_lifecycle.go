package agent

import (
	"fmt"
	"time"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
)

// completeDelegation returns the succeeded terminal form of a started
// delegation. The input record is never modified.
func completeDelegation(
	record DelegationExecutionRecord,
	finishedAt time.Time,
) (DelegationExecutionRecord, error) {
	if err := requireRunningDelegation(record); err != nil {
		return record, err
	}
	next := record
	next.Status = audit.StatusSucceeded
	next.FinishedAt = finishedAt
	next.DurationMS = finishedAt.Sub(record.StartedAt).Milliseconds()
	return next, nil
}

// failDelegation returns the failed terminal form of a started delegation.
func failDelegation(
	record DelegationExecutionRecord,
	finishedAt time.Time,
	code string,
	message string,
) (DelegationExecutionRecord, error) {
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
	return domain.NewError(
		domain.CodeDelegationFailed,
		fmt.Sprintf("delegation for agent %q is already terminal", record.TargetAgent),
		nil,
	)
}
