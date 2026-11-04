package evidence

import (
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
)

var lifecycleNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func startedToolCall() ToolCallRecord {
	return ToolCallRecord{CallID: "call_1", Tool: "test_tool", Status: audit.StatusRunning, StartedAt: lifecycleNow.Add(-time.Second)}
}

func TestSucceedToolCallDoesNotMutatePreviousRecord(t *testing.T) {
	started := startedToolCall()
	result := map[string]any{"ok": true}

	next, err := CompleteToolCall(started, result, lifecycleNow)
	if err != nil {
		t.Fatalf("CompleteToolCall() error = %v", err)
	}
	if next.Status != audit.StatusSucceeded || next.DurationMS != 1000 || next.OutputBytes == 0 {
		t.Fatalf("next record = %#v", next)
	}
	if started.Status != audit.StatusRunning || started.FinishedAt != (time.Time{}) {
		t.Fatalf("previous record = %#v, want unchanged", started)
	}
}

func TestFailToolCallDoesNotMutatePreviousRecord(t *testing.T) {
	started := startedToolCall()
	next, err := FailToolCall(started, lifecycleNow, domain.CodeToolExecutionFailed, "failed")
	if err != nil {
		t.Fatalf("FailToolCall() error = %v", err)
	}
	if next.Status != audit.StatusFailed || next.ErrorCode != domain.CodeToolExecutionFailed {
		t.Fatalf("next record = %#v", next)
	}
	if started.Status != audit.StatusRunning || started.ErrorCode != "" {
		t.Fatalf("previous record = %#v, want unchanged", started)
	}
}

func startedDelegation() DelegationExecutionRecord {
	return DelegationExecutionRecord{TargetAgent: "worker", Status: audit.StatusRunning, StartedAt: lifecycleNow.Add(-time.Second)}
}

func TestCompleteDelegationDoesNotMutatePreviousRecord(t *testing.T) {
	started := startedDelegation()
	next, err := CompleteDelegation(started, lifecycleNow)
	if err != nil {
		t.Fatalf("CompleteDelegation() error = %v", err)
	}
	if next.Status != audit.StatusSucceeded || next.DurationMS != 1000 {
		t.Fatalf("next record = %#v", next)
	}
	if started.Status != audit.StatusRunning || started.FinishedAt != (time.Time{}) {
		t.Fatalf("previous record = %#v, want unchanged", started)
	}
}

func TestFailDelegationRejectsTerminalRecord(t *testing.T) {
	started := startedDelegation()
	terminal, err := FailDelegation(started, lifecycleNow, domain.CodeDelegationFailed, "failed")
	if err != nil {
		t.Fatalf("failDelegation() error = %v", err)
	}
	if _, err := FailDelegation(terminal, lifecycleNow, domain.CodeDelegationFailed, "failed again"); err == nil {
		t.Fatal("FailDelegation() unexpectedly accepted a terminal record")
	}
}
