package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// RecoverAbandonedTasks deterministically closes non-terminal protocol tasks
// left active by a previous process. A single-process runtime cannot resume
// their in-memory ADK work, so A2A's standard failed state is the truthful
// terminal transition. This runs before the server accepts traffic.
func (db *DB) RecoverAbandonedTasks(ctx context.Context, now time.Time) (int, error) {
	if db == nil || db.sql == nil {
		return 0, fmt.Errorf("protocol state database is closed")
	}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT task_id, tenant, principal, owner_key, version, task_json
		FROM a2a_tasks
		WHERE state IN (?, ?, ?, ?, ?)
		ORDER BY task_id`,
		string(a2a.TaskStateUnspecified),
		string(a2a.TaskStateSubmitted),
		string(a2a.TaskStateWorking),
		string(a2a.TaskStateInputRequired),
		string(a2a.TaskStateAuthRequired),
	)
	if err != nil {
		return 0, fmt.Errorf("find abandoned tasks: %w", err)
	}
	type abandonedTask struct {
		taskID    a2a.TaskID
		tenant    string
		principal string
		ownerKey  string
		version   int64
		task      a2a.Task
	}
	var tasks []abandonedTask
	for rows.Next() {
		var item abandonedTask
		var raw string
		if err := rows.Scan(&item.taskID, &item.tenant, &item.principal, &item.ownerKey, &item.version, &raw); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan abandoned task: %w", err)
		}
		if err := json.Unmarshal([]byte(raw), &item.task); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("decode abandoned task %s: %w", item.taskID, err)
		}
		item.taskID = a2a.TaskID(item.task.ID)
		tasks = append(tasks, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate abandoned tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close abandoned task rows: %w", err)
	}

	recovered := 0
	for _, item := range tasks {
		item.task.Status.State = a2a.TaskStateFailed
		item.task.Status.Timestamp = &now
		item.task.Status.Message = a2a.NewMessageForTask(
			a2a.MessageRoleAgent,
			&item.task,
			a2a.NewTextPart("runtime restarted before task completion"),
		)
		raw, err := json.Marshal(&item.task)
		if err != nil {
			return recovered, fmt.Errorf("encode recovered task %s: %w", item.taskID, err)
		}
		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return recovered, fmt.Errorf("begin recovery for task %s: %w", item.taskID, err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE a2a_tasks
			SET state = ?, status_at_unix = ?, updated_at_unix = ?, version = ?, task_json = ?
			WHERE task_id = ? AND owner_key = ? AND version = ?
			  AND state IN (?, ?, ?, ?, ?)`,
			string(a2a.TaskStateFailed), now.UnixNano(), now.UnixNano(), item.version+1, string(raw),
			string(item.taskID), item.ownerKey, item.version,
			string(a2a.TaskStateUnspecified), string(a2a.TaskStateSubmitted), string(a2a.TaskStateWorking),
			string(a2a.TaskStateInputRequired), string(a2a.TaskStateAuthRequired),
		)
		if err != nil {
			_ = tx.Rollback()
			return recovered, fmt.Errorf("recover task %s: %w", item.taskID, err)
		}
		if deleted, err := result.RowsAffected(); err == nil && deleted == 0 {
			_ = tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			return recovered, fmt.Errorf("commit recovery for task %s: %w", item.taskID, err)
		}
		recovered++
	}
	return recovered, nil
}
