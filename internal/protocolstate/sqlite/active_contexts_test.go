package sqlite

import (
	"context"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestActiveContextIDsExcludeTerminalStates(t *testing.T) {
	db, tasks, callbacks := openStore(t)
	ctx := callerContext(context.Background(), "alice")
	seedTask := func(id, contextID string, state a2a.TaskState) {
		t.Helper()
		if _, err := tasks.Create(ctx, testTask(id, contextID, state)); err != nil {
			t.Fatalf("Create(%s) error = %v", id, err)
		}
	}
	seedTask("working", "context-working", a2a.TaskStateWorking)
	seedTask("waiting", "context-waiting", a2a.TaskStateInputRequired)
	seedTask("completed", "context-completed", a2a.TaskStateCompleted)

	if _, err := callbacks.Save(ctx, "working", &a2a.PushConfig{URL: "https://receiver.example/callback"}); err != nil {
		t.Fatalf("Save(push config) error = %v", err)
	}
	if _, err := tasks.Create(ctx, testTask("rejected", "context-rejected", a2a.TaskStateRejected)); err != nil {
		t.Fatalf("Create(rejected) error = %v", err)
	}

	active, err := db.ActiveContextIDs(context.Background())
	if err != nil {
		t.Fatalf("ActiveContextIDs() error = %v", err)
	}
	want := map[string]struct{}{
		"context-working": {},
		"context-waiting": {},
	}
	if len(active) != len(want) {
		t.Fatalf("ActiveContextIDs() = %#v, want %#v", active, want)
	}
	for contextID := range want {
		if _, ok := active[contextID]; !ok {
			t.Fatalf("ActiveContextIDs() missing %q", contextID)
		}
	}
	if _, ok := active["context-completed"]; ok {
		t.Fatal("terminal context was returned as active")
	}
}

func TestActiveContextIDsFailsClosedWhenClosed(t *testing.T) {
	db := &DB{}
	if _, err := db.ActiveContextIDs(context.Background()); err == nil {
		t.Fatal("closed database was accepted")
	}
	if _, err := db.ActiveContextIDs(context.Background()); err == nil {
		t.Fatal("closed database was accepted on repeat")
	}
}
