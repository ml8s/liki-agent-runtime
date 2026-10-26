package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/agent"
	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

type recordingAuditEvents struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingAuditEvents) Record(_ context.Context, event *audit.Event) error {
	if event == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *event)
	return nil
}

func (r *recordingAuditEvents) eventsOfType(eventType audit.EventType) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]audit.Event, 0)
	for _, event := range r.events {
		if event.Type == eventType {
			result = append(result, event)
		}
	}
	return result
}

func newAuditRuntime(t *testing.T) (*agent.Runtime, *recordingAuditEvents) {
	t.Helper()
	events := &recordingAuditEvents{}
	deployment := agent.NewTestDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	runtime, err := agent.NewRuntime(agent.Config{
		Model:            "test-model",
		ModelAPIKey:      "test-key",
		AgentDeployment:  deployment,
		StructuredOutput: "json_schema",
		Provider:         "test-provider",
		AuditRecorder:    events,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime, events
}

// flakyRunTerminalAudit injects a configurable number of transient failures for
// terminal run events, leaving all other audit appends intact.
type flakyRunTerminalAudit struct {
	*recordingAuditEvents
	mu        sync.Mutex
	remaining int
}

func (r *flakyRunTerminalAudit) Record(ctx context.Context, event *audit.Event) error {
	if event != nil && (event.Type == audit.EventRunCompleted || event.Type == audit.EventRunFailed) {
		r.mu.Lock()
		if r.remaining > 0 {
			r.remaining--
			r.mu.Unlock()
			return errors.New("transient audit store failure")
		}
		r.mu.Unlock()
	}
	return r.recordingAuditEvents.Record(ctx, event)
}

func newAuditRuntimeWith(t *testing.T, recorder audit.Recorder, maxConcurrent int) *agent.Runtime {
	return newAuditRuntimeWithClock(t, recorder, maxConcurrent, nil)
}

func newAuditRuntimeWithClock(t *testing.T, recorder audit.Recorder, maxConcurrent int, now func() time.Time) *agent.Runtime {
	t.Helper()
	deployment := agent.NewTestDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	runtime, err := agent.NewRuntime(agent.Config{
		Model:             "test-model",
		ModelAPIKey:       "test-key",
		AgentDeployment:   deployment,
		StructuredOutput:  "json_schema",
		Provider:          "test-provider",
		AuditRecorder:     recorder,
		MaxConcurrentRuns: maxConcurrent,
		Now:               now,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func TestExternalAuditRunLifecycleIsExclusive(t *testing.T) {
	runtime, events := newAuditRuntime(t)
	scope := agent.RunAuditIdentity{RunID: "run_1", ThreadID: "thread_1", UserID: "user_1"}
	ctx := context.Background()
	if err := runtime.BeginAuditRun(ctx, "session_1", scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	if err := runtime.BeginAuditRun(ctx, "session_1", scope); err == nil {
		t.Fatal("second BeginAuditRun unexpectedly succeeded")
	}
	if got := len(events.eventsOfType(audit.EventRunStarted)); got != 1 {
		t.Fatalf("started events = %d, want 1", got)
	}
	if err := runtime.EndAuditRun(ctx, "session_1", nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v", err)
	}
	if got := len(events.eventsOfType(audit.EventRunCompleted)); got != 1 {
		t.Fatalf("completed events = %d, want 1", got)
	}
	scope.RunID = "run_2"
	if err := runtime.BeginAuditRun(ctx, "session_1", scope); err != nil {
		t.Fatalf("BeginAuditRun after EndAuditRun() error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, "session_1", nil); err != nil {
		t.Fatalf("EndAuditRun after session reuse() error = %v", err)
	}
}

func TestExternalAuditRunAllowsMultipleTasksInOneSession(t *testing.T) {
	runtime, events := newAuditRuntime(t)
	ctx := context.Background()
	for _, runID := range []string{"task_1", "task_2"} {
		scope := agent.RunAuditIdentity{
			RunID:    runID,
			ThreadID: "context_1",
			UserID:   "user_1",
			Protocol: "a2a",
		}
		if err := runtime.BeginAuditRun(ctx, "context_1", scope); err != nil {
			t.Fatalf("BeginAuditRun(%s) error = %v", runID, err)
		}
		if err := runtime.EndAuditRun(ctx, "context_1", nil); err != nil {
			t.Fatalf("EndAuditRun(%s) error = %v", runID, err)
		}
	}
	terminal := events.eventsOfType(audit.EventRunCompleted)
	if len(terminal) != 2 {
		t.Fatalf("terminal events = %d, want 2", len(terminal))
	}
	if terminal[0].ID == terminal[1].ID ||
		terminal[0].RunID == terminal[1].RunID ||
		terminal[0].Protocol != "a2a" || terminal[1].Protocol != "a2a" {
		t.Fatalf("terminal audit identities = %#v / %#v", terminal[0], terminal[1])
	}
	for _, event := range terminal {
		if event.AgentDefinitionDigest == "" ||
			event.DefinitionDigest == "" ||
			event.Model == "" ||
			event.Provider == "" {
			t.Fatalf("terminal audit lacks provenance: %#v", event)
		}
	}
}

func TestExternalAuditRunRejectsDuplicateRunID(t *testing.T) {
	runtime, _ := newAuditRuntime(t)
	ctx := context.Background()
	scope := agent.RunAuditIdentity{
		RunID:    "task_duplicate",
		ThreadID: "context_1",
		UserID:   "user_1",
		Protocol: "a2a",
	}
	if err := runtime.BeginAuditRun(ctx, "context_1", scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, "context_1", nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v", err)
	}
	err := runtime.BeginAuditRun(ctx, "context_2", scope)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRunIDConflict {
		t.Fatalf("second BeginAuditRun() error = %v, want %s", err, domain.CodeRunIDConflict)
	}
}

func TestExternalAuditRunRecordsDuration(t *testing.T) {
	events := &recordingAuditEvents{}
	tick := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	runtime := newAuditRuntimeWithClock(t, events, 1, func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	})
	ctx := context.Background()
	scope := agent.RunAuditIdentity{
		RunID:    "task_duration",
		ThreadID: "context_1",
		UserID:   "user_1",
		Protocol: "a2a",
	}
	if err := runtime.BeginAuditRun(ctx, "context_1", scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, "context_1", nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v", err)
	}
	completed := events.eventsOfType(audit.EventRunCompleted)
	if len(completed) != 1 || completed[0].DurationMS <= 0 {
		t.Fatalf("completed duration = %+v, want positive duration", completed)
	}
}

func TestExternalAuditRunRetriesTerminalAppendAndReleasesOnce(t *testing.T) {
	recorder := &flakyRunTerminalAudit{recordingAuditEvents: &recordingAuditEvents{}, remaining: 2}
	runtime := newAuditRuntimeWith(t, recorder, 1)
	ctx := context.Background()
	scope := agent.RunAuditIdentity{RunID: "task_retry", ThreadID: "context_1", UserID: "user_1", Protocol: "a2a"}
	if err := runtime.BeginAuditRun(ctx, "context_1", scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	// The first completion exhausts both append attempts and must not release
	// the run scope or the single concurrency slot.
	if err := runtime.EndAuditRun(ctx, "context_1", nil); err == nil {
		t.Fatal("EndAuditRun() unexpectedly succeeded despite terminal append failure")
	}
	if got := len(recorder.eventsOfType(audit.EventRunCompleted)); got != 0 {
		t.Fatalf("completed events after failure = %d, want 0", got)
	}
	// The executor's cleanup callback retries the terminal append and succeeds.
	if err := runtime.EndAuditRun(ctx, "context_1", nil); err != nil {
		t.Fatalf("retry EndAuditRun() error = %v", err)
	}
	if got := len(recorder.eventsOfType(audit.EventRunCompleted)); got != 1 {
		t.Fatalf("completed events after retry = %d, want 1", got)
	}
	// The slot was released exactly once: a fresh single-slot run acquires it
	// instead of observing CodeRuntimeBusy (release) or blocking (double release).
	next := agent.RunAuditIdentity{RunID: "task_next", ThreadID: "context_2", UserID: "user_1", Protocol: "a2a"}
	if err := runtime.BeginAuditRun(ctx, "context_2", next); err != nil {
		t.Fatalf("BeginAuditRun() after release error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, "context_2", nil); err != nil {
		t.Fatalf("EndAuditRun() after release error = %v", err)
	}
}

type existsAudit struct {
	recordingAuditEvents
	exists bool
}

func (e *existsAudit) RunExists(_ context.Context, _ domain.ID) (bool, error) {
	return e.exists, nil
}

// TestExternalAuditRunRejectsDurableDuplicateRunID covers the durable
// run-id check (ensureDurableRunID) that runs before in-memory bookkeeping.
func TestExternalAuditRunRejectsDurableDuplicateRunID(t *testing.T) {
	runtime := newAuditRuntimeWith(t, &existsAudit{exists: true}, 1)
	scope := agent.RunAuditIdentity{RunID: "run_dup", ThreadID: "thread_1", UserID: "user_1", Protocol: "a2a"}
	err := runtime.BeginAuditRun(context.Background(), "session_dup", scope)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRunIDConflict {
		t.Fatalf("BeginAuditRun() error = %v, want %s", err, domain.CodeRunIDConflict)
	}
}
