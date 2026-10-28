package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// RetentionResult reports one bounded retention sweep.
type RetentionResult struct {
	Tasks       int64
	PushConfigs int64
}

// RetainTerminalTasks transactionally removes terminal A2A tasks older than
// the retention period. Process-owned and client-owned wait states are never
// deleted. Push configs are removed with their owning task because they are
// delivery configuration for protocol state, not independent audit evidence.
func (db *DB) RetainTerminalTasks(ctx context.Context, retention time.Duration, now time.Time) (RetentionResult, error) {
	if db == nil || db.sql == nil {
		return RetentionResult{}, fmt.Errorf("protocol state database is closed")
	}
	if retention <= 0 {
		return RetentionResult{}, fmt.Errorf("retention must be greater than zero")
	}
	if now.IsZero() {
		return RetentionResult{}, fmt.Errorf("retention now is required")
	}

	terminalStates := []a2a.TaskState{
		a2a.TaskStateCompleted,
		a2a.TaskStateCanceled,
		a2a.TaskStateFailed,
		a2a.TaskStateRejected,
	}
	cutoff := now.Add(-retention).UnixNano()
	placeholders := placeholdersFor(len(terminalStates))
	args := make([]any, 0, len(terminalStates)+1)
	args = append(args, cutoff)
	for _, state := range terminalStates {
		args = append(args, state.String())
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("begin terminal task retention: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	pushResult, err := tx.ExecContext(ctx, `
		DELETE FROM a2a_push_configs
		WHERE task_id IN (
			SELECT task_id FROM a2a_tasks
			WHERE updated_at_unix < ? AND state IN (`+placeholders+`)
		)`, args...)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("delete expired terminal push configs: %w", err)
	}
	pushConfigs, err := pushResult.RowsAffected()
	if err != nil {
		return RetentionResult{}, fmt.Errorf("count expired terminal push configs: %w", err)
	}

	taskResult, err := tx.ExecContext(ctx, `
		DELETE FROM a2a_tasks
		WHERE updated_at_unix < ? AND state IN (`+placeholders+`)`, args...)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("delete expired terminal tasks: %w", err)
	}
	tasks, err := taskResult.RowsAffected()
	if err != nil {
		return RetentionResult{}, fmt.Errorf("count expired terminal tasks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RetentionResult{}, fmt.Errorf("commit terminal task retention: %w", err)
	}
	return RetentionResult{Tasks: tasks, PushConfigs: pushConfigs}, nil
}

func placeholdersFor(count int) string {
	placeholders := make([]byte, 0, count*3)
	for index := range count {
		if index > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
	}
	return string(placeholders)
}
