package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

func openStore(t *testing.T) (*DB, *TaskStore, *PushConfigStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "protocol.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, db.NewTaskStore(TaskStoreConfig{}), db.NewPushConfigStore(PushConfigStoreConfig{})
}

func callerContext(ctx context.Context, user string) context.Context {
	ctx, callCtx := a2asrv.NewCallContext(ctx, nil)
	callCtx.User = a2asrv.NewAuthenticatedUser(user, nil)
	return ctx
}

func testTask(id, contextID string, state a2a.TaskState) *a2a.Task {
	status := a2a.TaskStatus{State: state}
	if !state.Terminal() && state != a2a.TaskStateUnspecified {
		now := time.Date(2026, 10, 27, 10, 0, 0, 123456789, time.UTC)
		status.Timestamp = &now
	}
	task := &a2a.Task{
		ID:        a2a.TaskID(id),
		ContextID: contextID,
		Status:    status,
		History: []*a2a.Message{
			{ID: id + "-1", Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart("one")}},
			{ID: id + "-2", Role: a2a.MessageRoleAgent, Parts: a2a.ContentParts{a2a.NewTextPart("two")}},
		},
		Artifacts: []*a2a.Artifact{{
			ID:   a2a.ArtifactID(id + "-artifact"),
			Name: "answer",
			Parts: a2a.ContentParts{
				a2a.NewDataPart(map[string]any{"answer": "two"}),
			},
		}},
	}
	return task
}

func TestSQLiteStateSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "protocol.db")
	db, tasks, callbacks, err := openStores(path)
	if err != nil {
		t.Fatalf("open stores = %v", err)
	}
	ctx := callerContext(context.Background(), "alice")
	task := testTask("task-1", "context-1", a2a.TaskStateWorking)
	if _, err := tasks.Create(ctx, task); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	saved, err := callbacks.Save(ctx, "task-1", &a2a.PushConfig{URL: "https://receiver.example/callback", Token: "token"})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	db, tasks, callbacks, err = openStores(path)
	if err != nil {
		t.Fatalf("reopen stores = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	got, err := tasks.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("Get() after reopen error = %v", err)
	}
	if got.Task.ID != task.ID || got.Task.ContextID != task.ContextID || got.Version != 1 {
		t.Fatalf("reopened task = %+v/%d", got.Task, got.Version)
	}
	if len(got.Task.History) != 2 || len(got.Task.Artifacts) != 1 {
		t.Fatalf("reopened aggregate history/artifacts = %d/%d", len(got.Task.History), len(got.Task.Artifacts))
	}
	configs, err := callbacks.List(ctx, "task-1")
	if err != nil {
		t.Fatalf("List() after reopen error = %v", err)
	}
	if len(configs) != 1 || configs[0].ID != saved.ID || configs[0].Token != "token" {
		t.Fatalf("reopened configs = %#v", configs)
	}
}

func openStores(path string) (*DB, *TaskStore, *PushConfigStore, error) {
	db, err := Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	return db, db.NewTaskStore(TaskStoreConfig{}), db.NewPushConfigStore(PushConfigStoreConfig{}), nil
}

func TestTaskStoreOptimisticConcurrencyAndOwnership(t *testing.T) {
	_, tasks, _ := openStore(t)
	ctx := callerContext(context.Background(), "alice")
	otherCtx := callerContext(context.Background(), "bob")
	task := testTask("task-occ", "context-occ", a2a.TaskStateWorking)
	version, err := tasks.Create(ctx, task)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := tasks.Create(ctx, task); !errors.Is(err, taskstore.ErrTaskAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v, want already exists", err)
	}

	task.Status.State = a2a.TaskStateCompleted
	now := time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC)
	task.Status.Timestamp = &now
	updated, err := tasks.Update(ctx, &taskstore.UpdateRequest{
		Task:        task,
		PrevVersion: version,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated != version+1 {
		t.Fatalf("Update() version = %d, want %d", updated, version+1)
	}
	if _, err := tasks.Update(otherCtx, &taskstore.UpdateRequest{
		Task:        task,
		PrevVersion: updated,
	}); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("cross-owner Update() error = %v, want not found", err)
	}
	if _, err := tasks.Update(ctx, &taskstore.UpdateRequest{
		Task:        task,
		PrevVersion: version,
	}); !errors.Is(err, taskstore.ErrConcurrentModification) {
		t.Fatalf("stale Update() error = %v, want concurrent modification", err)
	}
	got, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Task.Status.State != a2a.TaskStateCompleted || got.Version != updated {
		t.Fatalf("Get() = %s/%d, want completed/%d", got.Task.Status.State, got.Version, updated)
	}
	if _, err := tasks.Get(otherCtx, task.ID); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("cross-owner Get() error = %v, want not found", err)
	}
}

func TestTaskStoreListFiltersProjectionsAndPagination(t *testing.T) {
	_, tasks, _ := openStore(t)
	ctx := callerContext(context.Background(), "alice")
	otherCtx := callerContext(context.Background(), "bob")
	for _, task := range []*a2a.Task{
		testTask("task-a", "context-a", a2a.TaskStateWorking),
		testTask("task-b", "context-a", a2a.TaskStateCompleted),
		testTask("task-c", "context-b", a2a.TaskStateWorking),
		testTask("task-d", "context-a", a2a.TaskStateWorking),
	} {
		if _, err := tasks.Create(ctx, task); err != nil {
			t.Fatalf("Create(%s) error = %v", task.ID, err)
		}
	}
	if _, err := tasks.Create(otherCtx, testTask("task-other", "context-a", a2a.TaskStateWorking)); err != nil {
		t.Fatalf("Create(other) error = %v", err)
	}

	status := a2a.TaskStateWorking
	historyLength := 1
	first, err := tasks.List(ctx, &a2a.ListTasksRequest{
		ContextID:        "context-a",
		Status:           status,
		PageSize:         1,
		HistoryLength:    &historyLength,
		IncludeArtifacts: false,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if first.TotalSize != 2 || len(first.Tasks) != 1 || first.Tasks[0].ID != "task-d" {
		t.Fatalf("first list = %+v", first)
	}
	if len(first.Tasks[0].History) != 1 || first.Tasks[0].Artifacts != nil {
		t.Fatalf("list projections history/artifacts = %d/%v", len(first.Tasks[0].History), first.Tasks[0].Artifacts)
	}

	second, err := tasks.List(ctx, &a2a.ListTasksRequest{
		ContextID: "context-a",
		Status:    status,
		PageSize:  1,
		PageToken: first.NextPageToken,
	})
	if err != nil {
		t.Fatalf("List(page 2) error = %v", err)
	}
	// The context-a working page has only task-a; its opaque token must not
	// accidentally expose another owner's or another context's task.
	if len(second.Tasks) != 1 || second.Tasks[0].ID != "task-a" {
		t.Fatalf("second list = %#v, want task-a", second.Tasks)
	}
	all, err := tasks.List(ctx, &a2a.ListTasksRequest{PageSize: 100})
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	if all.TotalSize != 4 {
		t.Fatalf("all list total = %d, want 4", all.TotalSize)
	}
}

func TestPushConfigStoreCRUDAndOwnership(t *testing.T) {
	_, tasks, callbacks := openStore(t)
	ctx := callerContext(context.Background(), "alice")
	otherCtx := callerContext(context.Background(), "bob")
	task := testTask("task-push", "context-push", a2a.TaskStateWorking)
	if _, err := tasks.Create(ctx, task); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	inline, err := callbacks.Save(ctx, task.ID, &a2a.PushConfig{URL: "https://receiver.example/inline", Token: "inline"})
	if err != nil {
		t.Fatalf("Save(inline) error = %v", err)
	}
	if inline.ID == "" || inline.TaskID != task.ID {
		t.Fatalf("Save(inline) = %#v, want generated ID and forced task", inline)
	}
	created, err := callbacks.Save(ctx, task.ID, &a2a.PushConfig{ID: "config-2", URL: "https://receiver.example/created"})
	if err != nil {
		t.Fatalf("Save(created) error = %v", err)
	}
	if created.ID != "config-2" {
		t.Fatalf("Save(created) ID = %q", created.ID)
	}
	configs, err := callbacks.List(ctx, task.ID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("List() = %#v, want two configs", configs)
	}
	if _, err := callbacks.Get(otherCtx, task.ID, inline.ID); !errors.Is(err, push.ErrPushConfigNotFound) {
		t.Fatalf("cross-owner Get() error = %v, want not found", err)
	}
	if err := callbacks.Delete(otherCtx, task.ID, inline.ID); !errors.Is(err, push.ErrPushConfigNotFound) {
		t.Fatalf("cross-owner Delete() error = %v, want not found", err)
	}
	if err := callbacks.Delete(ctx, task.ID, created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	configs, err = callbacks.List(ctx, task.ID)
	if err != nil {
		t.Fatalf("List() after delete error = %v", err)
	}
	if len(configs) != 1 || configs[0].ID != inline.ID {
		t.Fatalf("List() after delete = %#v", configs)
	}
	if err := callbacks.DeleteAll(ctx, task.ID); err != nil {
		t.Fatalf("DeleteAll() error = %v", err)
	}
	configs, err = callbacks.List(ctx, task.ID)
	if err != nil {
		t.Fatalf("List() after DeleteAll error = %v", err)
	}
	if len(configs) != 0 {
		t.Fatalf("List() after DeleteAll = %#v, want empty", configs)
	}
}

func TestSQLiteProtocolStateHealth(t *testing.T) {
	db, _, _ := openStore(t)
	health := db.CheckHealth(context.Background())
	if !health.OK {
		t.Fatalf("CheckHealth() = %#v, want healthy", health)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if health := db.CheckHealth(context.Background()); health.OK {
		t.Fatal("CheckHealth() after close reported healthy")
	}
}

func TestRecoverAbandonedTasks(t *testing.T) {
	db, tasks, _ := openStore(t)
	ctx := callerContext(context.Background(), "alice")
	processOwnedStates := []a2a.TaskState{
		a2a.TaskStateSubmitted,
		a2a.TaskStateWorking,
	}
	waitingStates := []a2a.TaskState{
		a2a.TaskStateInputRequired,
		a2a.TaskStateAuthRequired,
	}
	for index, state := range processOwnedStates {
		if _, err := tasks.Create(ctx, testTask(fmt.Sprintf("active-%d", index), "context", state)); err != nil {
			t.Fatalf("Create(%s) error = %v", state, err)
		}
	}
	for index, state := range waitingStates {
		if _, err := tasks.Create(ctx, testTask(fmt.Sprintf("waiting-%d", index), "context", state)); err != nil {
			t.Fatalf("Create(%s) error = %v", state, err)
		}
	}
	if _, err := tasks.Create(ctx, testTask("terminal", "context", a2a.TaskStateCompleted)); err != nil {
		t.Fatalf("Create(terminal) error = %v", err)
	}

	recoveredAt := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	recovered, err := db.RecoverAbandonedTasks(ctx, recoveredAt)
	if err != nil {
		t.Fatalf("RecoverAbandonedTasks() error = %v", err)
	}
	if recovered != len(processOwnedStates) {
		t.Fatalf("RecoverAbandonedTasks() = %d, want %d", recovered, len(processOwnedStates))
	}
	for index := range processOwnedStates {
		got, err := tasks.Get(ctx, a2a.TaskID(fmt.Sprintf("active-%d", index)))
		if err != nil {
			t.Fatalf("Get(active-%d) error = %v", index, err)
		}
		if got.Task.Status.State != a2a.TaskStateFailed {
			t.Fatalf("active-%d state = %s, want failed", index, got.Task.Status.State)
		}
		if got.Task.Status.Timestamp == nil || !got.Task.Status.Timestamp.Equal(recoveredAt) {
			t.Fatalf("active-%d recovered at = %#v", index, got.Task.Status.Timestamp)
		}
	}
	for index, state := range waitingStates {
		taskID := a2a.TaskID(fmt.Sprintf("waiting-%d", index))
		got, err := tasks.Get(ctx, taskID)
		if err != nil {
			t.Fatalf("Get(waiting %s) error = %v", state, err)
		}
		if got.Task.Status.State != state {
			t.Fatalf("waiting task %s = %s, want preserved", taskID, got.Task.Status.State)
		}
	}
	got, err := tasks.Get(ctx, "terminal")
	if err != nil {
		t.Fatalf("Get(terminal) error = %v", err)
	}
	if got.Task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("terminal state = %s, want completed", got.Task.Status.State)
	}
}

func TestProtocolStateStats(t *testing.T) {
	db, tasks, callbacks := openStore(t)
	ctx := callerContext(context.Background(), "alice")
	if _, err := tasks.Create(ctx, testTask("stats-working", "context", a2a.TaskStateWorking)); err != nil {
		t.Fatalf("Create(working) error = %v", err)
	}
	if _, err := tasks.Create(ctx, testTask("stats-completed", "context", a2a.TaskStateCompleted)); err != nil {
		t.Fatalf("Create(completed) error = %v", err)
	}
	if _, err := callbacks.Save(ctx, "stats-working", &a2a.PushConfig{URL: "https://receiver.example/callback"}); err != nil {
		t.Fatalf("Save(push config) error = %v", err)
	}

	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.TaskCounts[string(a2a.TaskStateWorking)] != 1 ||
		stats.TaskCounts[string(a2a.TaskStateCompleted)] != 1 ||
		len(stats.TaskCounts) != 2 {
		t.Fatalf("Stats() task counts = %#v, want one working and one completed", stats.TaskCounts)
	}
	if stats.PushConfigs != 1 {
		t.Fatalf("Stats() push configs = %d, want 1", stats.PushConfigs)
	}
}
