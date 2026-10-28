package sqlite

import (
	"context"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// ActiveContextIDs returns ContextIDs that own non-terminal protocol work.
// They must remain available to A2A continuation even when session retention is
// enabled.
func (db *DB) ActiveContextIDs(ctx context.Context) (map[string]struct{}, error) {
	if db == nil || db.sql == nil {
		return nil, fmt.Errorf("protocol state database is closed")
	}
	waitingStates := []a2a.TaskState{
		a2a.TaskStateUnspecified,
		a2a.TaskStateSubmitted,
		a2a.TaskStateWorking,
		a2a.TaskStateInputRequired,
		a2a.TaskStateAuthRequired,
	}
	args := make([]any, 0, len(waitingStates))
	for _, state := range waitingStates {
		args = append(args, state.String())
	}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT DISTINCT context_id
		FROM a2a_tasks
		WHERE state IN (`+placeholdersFor(len(waitingStates))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("list active protocol contexts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	contexts := make(map[string]struct{})
	for rows.Next() {
		var contextID string
		if err := rows.Scan(&contextID); err != nil {
			return nil, fmt.Errorf("scan active protocol context: %w", err)
		}
		if contextID != "" {
			contexts[contextID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active protocol contexts: %w", err)
	}
	return contexts, nil
}
