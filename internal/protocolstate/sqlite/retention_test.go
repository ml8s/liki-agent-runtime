package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestRetainTerminalTasksRemovesOnlyExpiredTerminalAggregates(t *testing.T) {
	db, _, _ := openStore(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	recent := now.Add(-time.Minute)

	oldCaller := callerContext(context.Background(), "alice")

	oldTasks := db.NewTaskStore(TaskStoreConfig{Now: func() time.Time { return old }})
	newTasks := db.NewTaskStore(TaskStoreConfig{Now: func() time.Time { return recent }})
	oldPush := db.NewPushConfigStore(PushConfigStoreConfig{Now: func() time.Time { return old }})

	seed := func(store *TaskStore, id string, state a2a.TaskState) {
		t.Helper()
		if _, err := store.Create(oldCaller, testTask(id, "context", state)); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}
	seed(oldTasks, "expired-completed", a2a.TaskStateCompleted)
	seed(oldTasks, "expired-working", a2a.TaskStateWorking)
	seed(oldTasks, "expired-waiting", a2a.TaskStateInputRequired)
	seed(newTasks, "recent-completed", a2a.TaskStateCompleted)
	seed(newTasks, "recent-terminal", a2a.TaskStateRejected)
	if _, err := oldTasks.Create(oldCaller, testTask("expired-failed", "context", a2a.TaskStateFailed)); err != nil {
		t.Fatalf("Create(expired-failed) error = %v", err)
	}
	if _, err := oldPush.Save(oldCaller, "expired-completed", &a2a.PushConfig{URL: "https://receiver.example/callback"}); err != nil {
		t.Fatalf("Save(push config) error = %v", err)
	}

	result, err := db.RetainTerminalTasks(context.Background(), time.Hour, now)
	if err != nil {
		t.Fatalf("RetainTerminalTasks() error = %v", err)
	}
	if result.Tasks != 2 || result.PushConfigs != 1 {
		t.Fatalf("retention result = %+v, want two tasks and one push config", result)
	}

	ctx := callerContext(context.Background(), "alice")
	for taskID, want := range map[a2a.TaskID]bool{
		"expired-completed": false,
		"expired-failed":    false,
		"expired-working":   true,
		"expired-waiting":   true,
		"recent-completed":  true,
		"recent-terminal":   true,
	} {
		_, err := newTasks.Get(ctx, taskID)
		if want && err != nil {
			t.Fatalf("Get(%s) error = %v, want retained", taskID, err)
		}
		if !want && !errors.Is(err, a2a.ErrTaskNotFound) {
			t.Fatalf("Get(%s) error = %v, want not found", taskID, err)
		}
	}
}

func TestRetainTerminalTasksFailsClosed(t *testing.T) {
	db, _, _ := openStore(t)
	if _, err := db.RetainTerminalTasks(context.Background(), 0, time.Now()); err == nil {
		t.Fatal("zero retention was accepted")
	}
	if _, err := db.RetainTerminalTasks(context.Background(), time.Hour, time.Time{}); err == nil {
		t.Fatal("missing now was accepted")
	}
	closed := &DB{}
	if _, err := closed.RetainTerminalTasks(context.Background(), time.Hour, time.Now()); err == nil {
		t.Fatal("closed database was accepted")
	}
}
