package a2a

import (
	"context"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

func TestExecutionAttemptWithIDUsesServerIDWhenMessageIDMissing(t *testing.T) {
	request := &a2asrv.ExecutorContext{
		TaskID:    "task-1",
		ContextID: "context-1",
		Message:   &a2a.Message{},
	}
	first := executionAttemptFromRequest(request).withID()
	second := executionAttemptFromRequest(request).withID()

	if first.ID == "" || second.ID == "" {
		t.Fatalf("server attempt ids = %q/%q, want non-empty", first.ID, second.ID)
	}
	if first.ID == second.ID {
		t.Fatalf("distinct missing-message attempts reused id %q", first.ID)
	}
}

func TestExecutionAttemptFromContextReusesServerAttemptID(t *testing.T) {
	request := &a2asrv.ExecutorContext{
		TaskID:    "task-1",
		ContextID: "context-1",
		Message:   &a2a.Message{},
	}
	attempt := executionAttemptFromRequest(request).withID()
	ctx := context.WithValue(context.Background(), executionAttemptKey{}, attempt.ID)

	fromContext := executionAttemptFromContext(ctx, request)
	if fromContext.ID != attempt.ID {
		t.Fatalf("attempt id from context = %q, want %q", fromContext.ID, attempt.ID)
	}
}

func TestExecutionAttemptWithIDIsStableForClientMessageID(t *testing.T) {
	request := &a2asrv.ExecutorContext{
		TaskID:    "task-1",
		ContextID: "context-1",
		Message:   &a2a.Message{ID: "message-1"},
	}
	first := executionAttemptFromRequest(request).withID()
	second := executionAttemptFromRequest(request).withID()

	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("client-message attempt ids = %q/%q, want stable", first.ID, second.ID)
	}
}
