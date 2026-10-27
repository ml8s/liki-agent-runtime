package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

// TaskStore implements the official A2A taskstore.Store port with optimistic
// concurrency and caller-scoped reads.
type TaskStore struct {
	db  *sql.DB
	now func() time.Time
}

// TaskStoreConfig customizes the durable task store.
type TaskStoreConfig struct {
	Now func() time.Time
}

// NewTaskStore returns the reference durable task store.
func (db *DB) NewTaskStore(config TaskStoreConfig) *TaskStore {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &TaskStore{db: db.sql, now: config.Now}
}

var _ taskstore.Store = (*TaskStore)(nil)

// Create stores version 1 of a task.
func (s *TaskStore) Create(ctx context.Context, task *a2a.Task) (taskstore.TaskVersion, error) {
	if task == nil || task.ID == "" {
		return taskstore.TaskVersionMissing, fmt.Errorf("%w: task id is required", a2a.ErrInvalidParams)
	}
	owner, err := principalFromContext(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	raw, statusAt, err := encodeTask(task)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	now := s.now()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO a2a_tasks
			(task_id, tenant, principal, owner_key, context_id, state,
			 status_at_unix, updated_at_unix, version, task_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(task.ID), owner.Tenant, owner.User, owner.ownerKey, task.ContextID,
		string(task.Status.State), statusAt, now.UnixNano(), int64(taskstore.TaskVersion(1)), string(raw),
	)
	if isUniqueTaskError(err) {
		return taskstore.TaskVersionMissing, taskstore.ErrTaskAlreadyExists
	}
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("insert task: %w", err)
	}
	return taskstore.TaskVersion(1), nil
}

// Update applies one optimistic task transition.
func (s *TaskStore) Update(ctx context.Context, request *taskstore.UpdateRequest) (taskstore.TaskVersion, error) {
	if request == nil || request.Task == nil || request.Task.ID == "" {
		return taskstore.TaskVersionMissing, fmt.Errorf("%w: task id is required", a2a.ErrInvalidParams)
	}
	owner, err := principalFromContext(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	raw, statusAt, err := encodeTask(request.Task)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("begin task update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current taskstore.TaskVersion
	var storedOwner string
	err = tx.QueryRowContext(ctx, `
		SELECT version, owner_key
		FROM a2a_tasks
		WHERE task_id = ?`,
		string(request.Task.ID),
	).Scan(&current, &storedOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return taskstore.TaskVersionMissing, a2a.ErrTaskNotFound
	}
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("load task for update: %w", err)
	}
	if storedOwner != owner.ownerKey {
		return taskstore.TaskVersionMissing, a2a.ErrTaskNotFound
	}
	if request.PrevVersion != taskstore.TaskVersionMissing && request.PrevVersion != current {
		return taskstore.TaskVersionMissing, taskstore.ErrConcurrentModification
	}
	next := current + 1
	now := s.now()
	_, err = tx.ExecContext(ctx, `
		UPDATE a2a_tasks
		SET tenant = ?, principal = ?, owner_key = ?, context_id = ?, state = ?,
		    status_at_unix = ?, updated_at_unix = ?, version = ?, task_json = ?
		WHERE task_id = ? AND version = ?`,
		owner.Tenant, owner.User, owner.ownerKey, request.Task.ContextID,
		string(request.Task.Status.State), statusAt, now.UnixNano(), int64(next), string(raw),
		string(request.Task.ID), int64(current),
	)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("update task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("commit task update: %w", err)
	}
	return next, nil
}

// Get returns a task only to its owning principal.
func (s *TaskStore) Get(ctx context.Context, taskID a2a.TaskID) (*taskstore.StoredTask, error) {
	owner, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if taskID == "" {
		return nil, fmt.Errorf("%w: task id is required", a2a.ErrInvalidParams)
	}
	var (
		raw           string
		principalName string
		version       int64
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT task_json, principal, version
		FROM a2a_tasks
		WHERE task_id = ? AND owner_key = ?`,
		string(taskID), owner.ownerKey,
	).Scan(&raw, &principalName, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	var task a2a.Task
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		return nil, fmt.Errorf("decode task: %w", err)
	}
	return &taskstore.StoredTask{
		Task:    &task,
		Version: taskstore.TaskVersion(version),
		User:    principalName,
	}, nil
}

// List applies the standard A2A task filters, projections, and pagination.
func (s *TaskStore) List(ctx context.Context, request *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: list request is required", a2a.ErrInvalidParams)
	}
	owner, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	pageSize := request.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	if pageSize < 1 || pageSize > 100 {
		return nil, fmt.Errorf("%w: page size must be between 1 and 100", a2a.ErrInvalidRequest)
	}
	cursor, err := decodeTaskCursor(request.PageToken)
	if err != nil {
		return nil, err
	}

	conditions := []string{"owner_key = ?"}
	args := []any{owner.ownerKey}
	if request.ContextID != "" {
		conditions = append(conditions, "context_id = ?")
		args = append(args, request.ContextID)
	}
	if request.Status != a2a.TaskStateUnspecified {
		conditions = append(conditions, "state = ?")
		args = append(args, string(request.Status))
	}
	if request.StatusTimestampAfter != nil {
		// Tasks without a status timestamp are not excluded, matching the
		// reference filter where a nil timestamp cannot be Before the cutoff.
		conditions = append(conditions, "(status_at_unix IS NULL OR status_at_unix >= ?)")
		args = append(args, request.StatusTimestampAfter.UnixNano())
	}
	if cursor != nil {
		conditions = append(conditions, "(updated_at_unix < ? OR (updated_at_unix = ? AND task_id < ?))")
		args = append(args, cursor.UpdatedAtUnix, cursor.UpdatedAtUnix, cursor.TaskID)
	}

	var total int64
	countQuery := "SELECT count(*) FROM a2a_tasks WHERE " + strings.Join(conditions, " AND ")
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count tasks: %w", err)
	}

	// Ask for one extra row to distinguish a full final page from a page with
	// a successor without issuing a second data query.
	dataQuery := `
		SELECT task_json, updated_at_unix
		FROM a2a_tasks
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY updated_at_unix DESC, task_id DESC
		LIMIT ?`
	dataArgs := append(append([]any(nil), args...), pageSize+1)
	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	var tasks []*a2a.Task
	var lastUpdatedAtUnix int64
	for rows.Next() {
		var (
			raw           string
			updatedAtUnix int64
		)
		if err := rows.Scan(&raw, &updatedAtUnix); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan task: %w", err)
		}
		var task a2a.Task
		if err := json.Unmarshal([]byte(raw), &task); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("decode task: %w", err)
		}
		tasks = append(tasks, &task)
		lastUpdatedAtUnix = updatedAtUnix
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close task rows: %w", err)
	}

	var nextPageToken string
	if len(tasks) > pageSize {
		tasks = tasks[:pageSize]
		last := tasks[len(tasks)-1]
		nextPageToken = encodeTaskCursor(time.Unix(0, lastUpdatedAtUnix), last.ID)
	}
	projectTasks(tasks, request)
	return &a2a.ListTasksResponse{
		Tasks:         tasks,
		TotalSize:     int(total),
		PageSize:      pageSize,
		NextPageToken: nextPageToken,
	}, nil
}

func projectTasks(tasks []*a2a.Task, request *a2a.ListTasksRequest) {
	historyLength := 100
	if request.HistoryLength != nil {
		historyLength = *request.HistoryLength
	}
	for _, task := range tasks {
		switch {
		case historyLength <= 0:
			task.History = []*a2a.Message{}
		case len(task.History) > historyLength:
			task.History = task.History[len(task.History)-historyLength:]
		}
		if !request.IncludeArtifacts {
			task.Artifacts = nil
		}
	}
}

func encodeTask(task *a2a.Task) ([]byte, sql.NullInt64, error) {
	raw, err := json.Marshal(task)
	if err != nil {
		return nil, sql.NullInt64{}, fmt.Errorf("encode task: %w", err)
	}
	var statusAt sql.NullInt64
	if task.Status.Timestamp != nil {
		statusAt = sql.NullInt64{Int64: task.Status.Timestamp.UnixNano(), Valid: true}
	}
	return raw, statusAt, nil
}

type taskCursor struct {
	UpdatedAtUnix int64  `json:"u"`
	TaskID        string `json:"t"`
}

func encodeTaskCursor(updated time.Time, taskID a2a.TaskID) string {
	raw, _ := json.Marshal(taskCursor{UpdatedAtUnix: updated.UnixNano(), TaskID: string(taskID)})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeTaskCursor(token string) (*taskCursor, error) {
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid task page token", a2a.ErrParseError)
	}
	var cursor taskCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.TaskID == "" {
		return nil, fmt.Errorf("%w: invalid task page token", a2a.ErrParseError)
	}
	return &cursor, nil
}

func isUniqueTaskError(err error) bool {
	if err == nil {
		return false
	}
	// The pure-Go driver includes the constraint name in its error text. Keep
	// this mapping narrow instead of treating every constraint violation as a
	// duplicate aggregate.
	var sqliteErr interface{ Error() string }
	return errors.As(err, &sqliteErr) &&
		strings.Contains(sqliteErr.Error(), "UNIQUE constraint failed: a2a_tasks.task_id")
}
