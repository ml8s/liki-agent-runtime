package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agent "github.com/ml8s/agent-runtime/internal/agent"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
)

type recordingAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingAudit) Record(_ context.Context, event *audit.Event) error {
	if event == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *event)
	return nil
}

func (r *recordingAudit) eventsOfType(eventType audit.EventType) []audit.Event {
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

func newAuditRuntime(t *testing.T) (*agent.Runtime, *recordingAudit) {
	t.Helper()
	events := &recordingAudit{}
	agentDeployment := agent.NewTestDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	runtime, err := agent.NewRuntime(agent.Config{
		Model:            "test-model",
		ModelAPIKey:      "test-key",
		AgentDeployment:  agentDeployment,
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
	*recordingAudit
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
	return r.recordingAudit.Record(ctx, event)
}

func newAuditRuntimeWith(t *testing.T, recorder audit.Recorder, maxConcurrent int) *agent.Runtime {
	return newAuditRuntimeWithClock(t, recorder, maxConcurrent, nil)
}

func newAuditRuntimeWithClock(t *testing.T, recorder audit.Recorder, maxConcurrent int, now func() time.Time) *agent.Runtime {
	t.Helper()
	agentDeployment := agent.NewTestDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	runtime, err := agent.NewRuntime(agent.Config{
		Model:             "test-model",
		ModelAPIKey:       "test-key",
		AgentDeployment:   agentDeployment,
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

func externalAuditHandle(sessionID, attemptID string) agent.AuditAttemptHandle {
	return agent.AuditAttemptHandle{ContextID: sessionID, AttemptID: domain.AttemptID(attemptID)}
}

func TestExternalAuditRunLifecycleIsExclusive(t *testing.T) {
	runtime, events := newAuditRuntime(t)
	scope := agent.RunAuditIdentity{AttemptID: domain.AttemptID("run_1"), ThreadID: "thread_1", UserID: "user_1"}
	ctx := context.Background()
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle("session_1", string(scope.AttemptID)), scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle("session_1", string(scope.AttemptID)), scope); err == nil {
		t.Fatal("second BeginAuditRun unexpectedly succeeded")
	}
	if got := len(events.eventsOfType(audit.EventRunStarted)); got != 1 {
		t.Fatalf("started events = %d, want 1", got)
	}
	if err := runtime.EndAuditRun(ctx, externalAuditHandle("session_1", string(scope.AttemptID)), nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v", err)
	}
	if got := len(events.eventsOfType(audit.EventRunCompleted)); got != 1 {
		t.Fatalf("completed events = %d, want 1", got)
	}
	scope.AttemptID = domain.AttemptID("run_2")
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle("session_1", string(scope.AttemptID)), scope); err != nil {
		t.Fatalf("BeginAuditRun after EndAuditRun() error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, externalAuditHandle("session_1", string(scope.AttemptID)), nil); err != nil {
		t.Fatalf("EndAuditRun after session reuse() error = %v", err)
	}
}

func TestRuntimeRejectsConcurrencyAboveSharedLimit(t *testing.T) {
	recorder := &recordingAudit{}
	agentDeployment := agent.NewTestDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	_, err := agent.NewRuntime(agent.Config{
		Model:             "test-model",
		ModelAPIKey:       "test-key",
		AgentDeployment:   agentDeployment,
		StructuredOutput:  "json_schema",
		Provider:          "test-provider",
		AuditRecorder:     recorder,
		MaxConcurrentRuns: 1025,
	})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRuntimeInitFailed {
		t.Fatalf("NewRuntime() error = %v, want %s", err, domain.CodeRuntimeInitFailed)
	}
}

func TestExternalAuditRunAllowsMultipleTasksInOneSession(t *testing.T) {
	runtime, events := newAuditRuntime(t)
	ctx := context.Background()
	for _, runID := range []string{"task_1", "task_2"} {
		scope := agent.RunAuditIdentity{
			AttemptID: domain.AttemptID(runID),
			ThreadID:  "context_1",
			UserID:    "user_1",
			Protocol:  "a2a",
		}
		if err := runtime.BeginAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), scope); err != nil {
			t.Fatalf("BeginAuditRun(%s) error = %v", runID, err)
		}
		if err := runtime.EndAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), nil); err != nil {
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
		AttemptID: domain.AttemptID("task_duplicate"),
		ThreadID:  "context_1",
		UserID:    "user_1",
		Protocol:  "a2a",
	}
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v", err)
	}
	err := runtime.BeginAuditRun(ctx, externalAuditHandle("context_2", string(scope.AttemptID)), scope)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRunIDConflict {
		t.Fatalf("second BeginAuditRun() error = %v, want %s", err, domain.CodeRunIDConflict)
	}
}

func TestExternalAuditRunRecordsDuration(t *testing.T) {
	events := &recordingAudit{}
	tick := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	runtime := newAuditRuntimeWithClock(t, events, 1, func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	})
	ctx := context.Background()
	scope := agent.RunAuditIdentity{
		AttemptID: domain.AttemptID("task_duration"),
		ThreadID:  "context_1",
		UserID:    "user_1",
		Protocol:  "a2a",
	}
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v", err)
	}
	completed := events.eventsOfType(audit.EventRunCompleted)
	if len(completed) != 1 || completed[0].DurationMS <= 0 {
		t.Fatalf("completed duration = %+v, want positive duration", completed)
	}
}

func TestExternalAuditRunRetriesTerminalAppendAndReleasesOnce(t *testing.T) {
	recorder := &flakyRunTerminalAudit{recordingAudit: &recordingAudit{}, remaining: 2}
	runtime := newAuditRuntimeWith(t, recorder, 1)
	ctx := context.Background()
	scope := agent.RunAuditIdentity{AttemptID: domain.AttemptID("task_retry"), ThreadID: "context_1", UserID: "user_1", Protocol: "a2a"}
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	// The first completion exhausts both append attempts and must not release
	// the run scope or the single concurrency slot.
	if err := runtime.EndAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), nil); err == nil {
		t.Fatal("EndAuditRun() unexpectedly succeeded despite terminal append failure")
	}
	if got := len(recorder.eventsOfType(audit.EventRunCompleted)); got != 0 {
		t.Fatalf("completed events after failure = %d, want 0", got)
	}
	// The executor's cleanup callback retries the terminal append and succeeds.
	if err := runtime.EndAuditRun(ctx, externalAuditHandle("context_1", string(scope.AttemptID)), nil); err != nil {
		t.Fatalf("retry EndAuditRun() error = %v", err)
	}
	if got := len(recorder.eventsOfType(audit.EventRunCompleted)); got != 1 {
		t.Fatalf("completed events after retry = %d, want 1", got)
	}
	// The slot was released exactly once: a fresh single-slot run acquires it
	// instead of observing CodeRuntimeBusy (release) or blocking (double release).
	next := agent.RunAuditIdentity{AttemptID: domain.AttemptID("task_next"), ThreadID: "context_2", UserID: "user_1", Protocol: "a2a"}
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle("context_2", string(next.AttemptID)), next); err != nil {
		t.Fatalf("BeginAuditRun() after release error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, externalAuditHandle("context_2", string(next.AttemptID)), nil); err != nil {
		t.Fatalf("EndAuditRun() after release error = %v", err)
	}
}

type existsAudit struct {
	recordingAudit
	exists bool
}

func (e *existsAudit) RunExists(_ context.Context, _ domain.ID) (bool, error) {
	return e.exists, nil
}

// TestExternalAuditRunRejectsDurableDuplicateRunID covers the durable
// run-id check (ensureDurableRunID) that runs before in-memory bookkeeping.
func TestExternalAuditRunRejectsDurableDuplicateRunID(t *testing.T) {
	runtime := newAuditRuntimeWith(t, &existsAudit{exists: true}, 1)
	scope := agent.RunAuditIdentity{AttemptID: domain.AttemptID("run_dup"), ThreadID: "thread_1", UserID: "user_1", Protocol: "a2a"}
	err := runtime.BeginAuditRun(context.Background(), externalAuditHandle("session_dup", string(scope.AttemptID)), scope)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRunIDConflict {
		t.Fatalf("BeginAuditRun() error = %v, want %s", err, domain.CodeRunIDConflict)
	}
}

func TestEndAuditRunRecordsPausedAndCanceledAttempts(t *testing.T) {
	events := &recordingAudit{}
	agentDeployment := agent.NewTestDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	runtime, err := agent.NewRuntime(agent.Config{
		Model:           "test-model",
		ModelAPIKey:     "test-key",
		AgentDeployment: agentDeployment,
		Provider:        "test-provider",
		AuditRecorder:   events,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	ctx := context.Background()
	pausedScope := agent.RunAuditIdentity{
		AttemptID: domain.AttemptID("attempt_pause"), ThreadID: "context_1", UserID: "user_1",
		Protocol: "a2a", TaskID: "task_pause", MessageID: "message_pause",
	}
	pausedSession := "context_1"
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle(pausedSession, string(pausedScope.AttemptID)), pausedScope); err != nil {
		t.Fatalf("BeginAuditRun(paused) error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, externalAuditHandle(pausedSession, string(pausedScope.AttemptID)), nil,
		agent.WithPausedAttempt(), agent.WithPreservedSession()); err != nil {
		t.Fatalf("EndAuditRun(paused) error = %v", err)
	}
	paused := events.eventsOfType(audit.EventRunPaused)
	if len(paused) != 1 || paused[0].Status != audit.StatusPaused {
		t.Fatalf("paused events = %#v", paused)
	}
	if string(paused[0].ParentRunID) != "task_pause" {
		t.Fatalf("paused parent task = %q", paused[0].ParentRunID)
	}

	canceledScope := agent.RunAuditIdentity{
		AttemptID: domain.AttemptID("attempt_cancel"), ThreadID: "context_2", UserID: "user_1",
		Protocol: "a2a", TaskID: "task_cancel", MessageID: "message_cancel",
	}
	canceledSession := "context_2"
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle(canceledSession, string(canceledScope.AttemptID)), canceledScope); err != nil {
		t.Fatalf("BeginAuditRun(canceled) error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, externalAuditHandle(canceledSession, string(canceledScope.AttemptID)), nil,
		agent.WithCanceledAttempt(), agent.WithPreservedSession()); err != nil {
		t.Fatalf("EndAuditRun(canceled) error = %v", err)
	}
	canceled := events.eventsOfType(audit.EventRunCanceled)
	if len(canceled) != 1 || canceled[0].Status != audit.StatusCanceled {
		t.Fatalf("canceled events = %#v", canceled)
	}
	if string(canceled[0].ParentRunID) != "task_cancel" {
		t.Fatalf("canceled parent task = %q", canceled[0].ParentRunID)
	}
}
