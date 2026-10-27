package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// The agentDeployment is the single source of truth for whether structured output
// is used. An explicit mode with a plain-text agentDeployment carries no
// information and fails closed instead of being silently ignored.
func TestNewRuntimeRejectsRedundantStructuredOutputMode(t *testing.T) {
	_, err := NewRuntime(Config{
		Model:            "fake-model",
		AgentDeployment:  plainTestDeployment(t),
		StructuredOutput: execution.StructuredOutputJSONObject,
		AuditRecorder:    &recordingAudit{},
	})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStructuredOutputCapabilityInvalid {
		t.Fatalf("NewRuntime() error = %v, want %s", err, domain.CodeStructuredOutputCapabilityInvalid)
	}
}

func TestRuntimeRunReturnsPlainTextForGenericAgent(t *testing.T) {
	answer := "generic plain-text answer"
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: answer}}},
	}}})
	result, err := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_plain", ThreadID: "thread_plain", UserID: "user_1", UserMessage: "hello",
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != answer {
		t.Fatalf("Text = %q, want %q", result.Text, answer)
	}
	if len(result.Output) != 0 {
		t.Fatalf("Output = %s, want empty", result.Output)
	}
	if got := len(events.eventsOfType(audit.EventRunCompleted)); got != 1 {
		t.Fatalf("completed run audit events = %d, want 1", got)
	}
}

func TestRuntimeRunRejectsDuplicateRunID(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "first"}}},
	}}})
	request := execution.RunRequest{
		RunID: "run_duplicate", ThreadID: "thread_1", UserID: "user_1", UserMessage: "hello",
	}
	if _, err := runtime.Run(context.Background(), request, nil); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	_, err := runtime.Run(context.Background(), request, nil)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRunIDConflict {
		t.Fatalf("second Run() error = %v, want %s", err, domain.CodeRunIDConflict)
	}
}

func TestRuntimeRunRejectsWorkBeyondConcurrentRunLimit(t *testing.T) {
	events := &recordingAudit{}
	llm := &blockingLLM{started: make(chan struct{}), unblocked: make(chan struct{})}
	runtime := newTestRuntimeWithLimit(t, NewTestDeployment(t), events, llm, 1)
	firstDone := make(chan error, 1)
	go func() {
		_, err := runtime.Run(context.Background(), execution.RunRequest{
			RunID: "run_active", ThreadID: "thread_1", UserID: "user_1", UserMessage: "first",
		}, nil)
		firstDone <- err
	}()

	select {
	case <-llm.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not reach the model")
	}
	limitedCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := runtime.Run(limitedCtx, execution.RunRequest{
		RunID: "run_limited", ThreadID: "thread_1", UserID: "user_1", UserMessage: "second",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRuntimeBusy {
		t.Fatalf("limited Run() error = %v, want %s", err, domain.CodeRuntimeBusy)
	}
	close(llm.unblocked)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
}

func TestRuntimeRunDeletesRunScopedSession(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "complete"}}},
	}}})
	request := execution.RunRequest{
		RunID: "run_cleanup", ThreadID: "thread_1", UserID: "user_1", UserMessage: "hello",
	}
	if _, err := runtime.Run(context.Background(), request, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	response, err := runtime.sessions.Get(context.Background(), &session.GetRequest{
		AppName:   runtime.config.AppName,
		UserID:    request.UserID,
		SessionID: "run:" + request.RunID,
	})
	if err == nil && response != nil && response.Session != nil {
		t.Fatal("run-scoped ADK session survived terminal run")
	}
}

func TestRunAuditUsesEntrypointModelOverride(t *testing.T) {
	agentDeployment := NewTestDeployment(t)
	agentDeployment.Spec.Agents[0].Model = "artifact-model"
	events := &recordingAudit{}
	runtime := newTestRuntime(t, agentDeployment, events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"ok"}`}}},
	}}})

	if _, err := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_model", ThreadID: "thread_model", UserID: "user_1", UserMessage: "hello",
	}, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	started := events.eventsOfType(audit.EventRunStarted)
	if len(started) != 1 || started[0].Model != "artifact-model" {
		t.Fatalf("run started model = %#v, want artifact-model", started)
	}
}

type failingDeleteSessionService struct {
	session.Service
}

func externalAuditHandle(sessionID, attemptID string) AuditAttemptHandle {
	return AuditAttemptHandle{ADKSessionID: sessionID, AttemptID: attemptID}
}

func (s *failingDeleteSessionService) Delete(context.Context, *session.DeleteRequest) error {
	return errors.New("session backend unavailable")
}

func TestExternalSessionCleanupFailureDoesNotFlipSuccessfulRun(t *testing.T) {
	agentDeployment := NewTestDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	events := &recordingAudit{}
	runtime, err := NewRuntime(Config{
		Model:             "fake-model",
		AgentDeployment:   agentDeployment,
		AuditRecorder:     events,
		MaxConcurrentRuns: 1,
		modelOverride: &fakeLLM{responses: []*model.LLMResponse{{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"ok"}`}}},
		}}},
		SessionService: &failingDeleteSessionService{session.InMemoryService()},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	scope := RunAuditIdentity{
		RunID: "external_cleanup_failure", ThreadID: "external_context", UserID: "user_1", Protocol: "a2a",
	}
	if err := runtime.BeginAuditRun(context.Background(), externalAuditHandle("external_context", scope.RunID), scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	err = runtime.EndAuditRun(context.Background(), externalAuditHandle("external_context", scope.RunID), nil)
	if err != nil {
		t.Fatalf("EndAuditRun() error = %v, want successful execution despite cleanup failure", err)
	}
	completed := events.eventsOfType(audit.EventRunCompleted)
	failed := events.eventsOfType(audit.EventRunFailed)
	if len(completed) != 1 || len(failed) != 0 {
		t.Fatalf("run terminal events = completed:%d failed:%d, want 1/0", len(completed), len(failed))
	}
}

func TestExternalAuditRunDeletesExternalSession(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{})
	ctx := context.Background()
	scope := RunAuditIdentity{
		RunID:    "a2a_task",
		ThreadID: "a2a_context",
		UserID:   "user_1",
		Protocol: "a2a",
	}
	if err := runtime.BeginAuditRun(ctx, externalAuditHandle("a2a_context", scope.RunID), scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, externalAuditHandle("a2a_context", scope.RunID), nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v", err)
	}
	response, err := runtime.sessions.Get(ctx, &session.GetRequest{
		AppName:   runtime.config.AppName,
		UserID:    scope.UserID,
		SessionID: "a2a_context",
	})
	if err == nil && response != nil && response.Session != nil {
		t.Fatal("external ADK session survived terminal run")
	}
}

type terminalAuditRecorder struct {
	delegate *recordingAudit
}

func (r *terminalAuditRecorder) Record(_ context.Context, event *audit.Event) error {
	if event != nil && event.Type == audit.EventRunCompleted {
		return domain.NewError(audit.CodeAuditAppendFailed, "terminal audit write failed", nil)
	}
	return r.delegate.Record(context.Background(), event)
}

func TestRuntimeRunFailsClosedWhenTerminalAuditWriteFails(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "complete"}}},
	}}})
	failing := &terminalAuditRecorder{delegate: events}
	runtime.config.AuditRecorder = failing
	_, err := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_audit_fail", ThreadID: "thread_1", UserID: "user_1", UserMessage: "hello",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != audit.CodeAuditAppendFailed {
		t.Fatalf("Run() error = %v, want terminal audit failure", err)
	}
}

// TestRuntimeRunExecutesMCPToolBeforeAuditCompletion prevents an observability
// callback from accidentally replacing the tool invocation with its arguments.
func TestRuntimeRunExecutesMCPToolBeforeAuditCompletion(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &turnBasedLLM{turns: []*model.LLMResponse{
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "call_tool", Name: "test_tool"}},
		}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "final answer"}}}},
	}})

	result, err := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_tool", ThreadID: "thread_tool", UserID: "user_1", UserMessage: "use tool",
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "final answer" {
		t.Fatalf("Text = %q, want final answer", result.Text)
	}
	completed := events.eventsOfType(audit.EventToolCallCompleted)
	if len(completed) != 1 {
		t.Fatalf("completed tool audit events = %d, want 1", len(completed))
	}
	inputDigest, _ := completed[0].Payload["input_digest"].(string)
	outputDigest, _ := completed[0].Payload["output_digest"].(string)
	if inputDigest == "" || outputDigest == "" {
		t.Fatalf("tool audit digests = %#v", completed[0].Payload)
	}
	if inputDigest == outputDigest {
		t.Fatal("tool output digest equals input digest; ADK did not execute the tool")
	}
}

func TestRuntimeRunEmptyResponse(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: ""}}},
	}}})
	_, runErr := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_empty", ThreadID: "thread_1", UserID: "user_1", UserMessage: "看事业",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(runErr, &domainErr) || domainErr.Code != domain.CodeRuntimeEmptyResponse {
		t.Fatalf("Run() error = %v, want %s", runErr, domain.CodeRuntimeEmptyResponse)
	}
	if got := len(events.eventsOfType(audit.EventRunFailed)); got != 1 {
		t.Fatalf("failed run audit events = %d, want 1", got)
	}
}

func TestRuntimeRunMarksObserverFailureAsFailedRun(t *testing.T) {
	observerErr := errors.New("protocol write failed")
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{Text: `{"answer":"ok"}`},
			{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "test_tool"}},
		}},
	}}})
	_, runErr := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_observer", ThreadID: "thread_1", UserID: "user_1", UserMessage: "test",
	}, func(*session.Event) error {
		return observerErr
	})
	if !errors.Is(runErr, observerErr) {
		t.Fatalf("Run() error = %v, want observer error", runErr)
	}
	if got := len(events.eventsOfType(audit.EventRunFailed)); got != 1 {
		t.Fatalf("failed run audit events = %d, want 1", got)
	}
}

func TestRuntimeRunContextCancelled(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "答案"}}},
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, runErr := runtime.Run(ctx, execution.RunRequest{
		RunID: "run_cancel", ThreadID: "thread_1", UserID: "user_1", UserMessage: "看事业",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(runErr, &domainErr) || domainErr.Code != domain.CodeRuntimeCancelled {
		t.Fatalf("Run() error = %v, want %s", runErr, domain.CodeRuntimeCancelled)
	}
}

func TestRuntimeRunLLMError(t *testing.T) {
	events := &recordingAudit{}
	llmErr := errors.New("model overloaded")
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{err: llmErr})
	_, runErr := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_err", ThreadID: "thread_1", UserID: "user_1", UserMessage: "看事业",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(runErr, &domainErr) {
		t.Fatalf("Run() error = %v, want domain error", runErr)
	}
	if domainErr.Code != domain.CodeRuntimeFailed {
		t.Fatalf("error code = %s, want %s", domainErr.Code, domain.CodeRuntimeFailed)
	}
	if got := len(events.eventsOfType(audit.EventLLMCallFailed)); got != 1 {
		t.Fatalf("failed LLM audit events = %d, want 1", got)
	}
}

// TestRuntimeRunTerminalEvidenceAfterChildLifecycles guards the terminal write
// order on the AG-UI (Run) path: child lifecycle failures must be appended
// before the run's terminal event, matching EndAuditRun's ordering.
func TestRuntimeRunTerminalEvidenceAfterChildLifecycles(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{err: errors.New("model unavailable")})
	_, runErr := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_order", ThreadID: "thread_order", UserID: "user_1", UserMessage: "x",
	}, nil)
	if runErr == nil {
		t.Fatal("expected run failure")
	}
	indexOf := func(want audit.EventType) int {
		for index, event := range events.events {
			if event.Type == want {
				return index
			}
		}
		return -1
	}
	llmFailed := indexOf(audit.EventLLMCallFailed)
	runFailed := indexOf(audit.EventRunFailed)
	if llmFailed < 0 || runFailed < 0 {
		t.Fatalf("missing terminal evidence: llm.failed=%d run.failed=%d", llmFailed, runFailed)
	}
	if runFailed <= llmFailed {
		t.Fatalf("run.failed (idx %d) must follow llm.call.failed (idx %d)", runFailed, llmFailed)
	}
}
