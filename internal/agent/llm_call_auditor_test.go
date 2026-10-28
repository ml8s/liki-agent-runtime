package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
	"go.opentelemetry.io/otel/trace"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type fakeAgentContext struct {
	adkagent.Context
	sessionID      string
	invocationID   string
	agentName      string
	branch         string
	functionCallID string
	traceContext   context.Context
}

func (c *fakeAgentContext) SessionID() string      { return c.sessionID }
func (c *fakeAgentContext) InvocationID() string   { return c.invocationID }
func (c *fakeAgentContext) AgentName() string      { return c.agentName }
func (c *fakeAgentContext) Branch() string         { return c.branch }
func (c *fakeAgentContext) FunctionCallID() string { return c.functionCallID }
func (c *fakeAgentContext) Deadline() (time.Time, bool) {
	if c.traceContext == nil {
		return time.Time{}, false
	}
	return c.traceContext.Deadline()
}

func (c *fakeAgentContext) Done() <-chan struct{} {
	if c.traceContext == nil {
		return nil
	}
	return c.traceContext.Done()
}

func (c *fakeAgentContext) Err() error {
	if c.traceContext == nil {
		return nil
	}
	return c.traceContext.Err()
}

func (c *fakeAgentContext) Value(key any) any {
	if c.traceContext != nil {
		if value := c.traceContext.Value(key); value != nil {
			return value
		}
	}
	return context.Background().Value(key)
}

type failOnceAudit struct {
	recordingAudit
	eventType audit.EventType
	failed    bool
}

func (r *failOnceAudit) Record(ctx context.Context, event *audit.Event) error {
	if event != nil && event.Type == r.eventType && !r.failed {
		r.failed = true
		return errors.New("temporary audit store failure")
	}
	return r.recordingAudit.Record(ctx, event)
}

func newTestLedger(t *testing.T) (*llmCallAuditor, *recordingAudit) {
	t.Helper()
	recorder := &recordingAudit{}
	callAuditor := newLLMCallAuditor(recorder, nil, "test", NewTestDeployment(t), func() time.Time {
		return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	})
	return callAuditor, recorder
}

func fixedLedgerNow() time.Time {
	return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
}

func newTestScope() *runExecution {
	return &runExecution{
		identity: execution.RunIdentity{
			RunID:    domain.ID("run_1"),
			ThreadID: domain.ID("thread_1"),
			UserID:   "user_1",
		},
		model:             "test-model",
		instructionDigest: "test-instruction",
		definitionName:    "test-definition",
		definitionVersion: "1.0.0",
		definitionDigest:  "sha256:test",
		activeCalls:       make(map[string]llmCallRuntime),
	}
}

func ledgerHandle(sessionID string, scope *runExecution) AuditAttemptHandle {
	return AuditAttemptHandle{ContextID: sessionID, AttemptID: domain.AttemptID(scope.identity.RunID)}
}

func testCtx() *fakeAgentContext {
	return &fakeAgentContext{sessionID: "session_1", invocationID: "inv_1", agentName: "coordinator"}
}

func traceCtxWith(traceID, spanID string) context.Context {
	tid, _ := trace.TraceIDFromHex(traceID)
	sid, _ := trace.SpanIDFromHex(spanID)
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(context.Background(), spanContext)
}

// TestLedgerReconciledTerminalKeepsTrace guards DOMAIN invariant 9: a
// reconciled (interrupted) terminal event must carry the same trace as its
// started fact even when reconciliation runs without a live span context.
func TestLedgerReconciledTerminalKeepsTrace(t *testing.T) {
	const (
		traceID = "0102030405060708090a0b0c0d0e0f10"
		spanID  = "0102030405060708"
	)
	callAuditor, recorder := newTestLedger(t)
	scope := newTestScope()
	scope.provenance.TraceID = traceID
	scope.provenance.SpanID = spanID
	if !callAuditor.begin(ledgerHandle("session_trace", scope), scope) {
		t.Fatal("begin() returned false")
	}
	ctx := &fakeAgentContext{
		sessionID:    "session_trace",
		invocationID: "inv_trace",
		agentName:    "coordinator",
		traceContext: traceCtxWith(traceID, spanID),
	}
	if _, err := callAuditor.beforeModel(ctx, &model.LLMRequest{Model: "test-model"}); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	// Reconciliation runs without a live span (context.Background inside
	// failActive); the scope-carried trace must survive.
	callAuditor.end(ledgerHandle("session_trace", scope), nil)
	failed := recorder.eventsOfType(audit.EventLLMCallFailed)
	if len(failed) != 1 {
		t.Fatalf("failed events = %d, want 1", len(failed))
	}
	if failed[0].TraceID != traceID || failed[0].SpanID != spanID {
		t.Fatalf("reconciled trace = %s/%s, want %s/%s", failed[0].TraceID, failed[0].SpanID, traceID, spanID)
	}
}

func TestLedgerLifecycleCompletesNormally(t *testing.T) {
	callAuditor, recorder := newTestLedger(t)
	scope := newTestScope()
	if !callAuditor.begin(ledgerHandle("session_1", scope), scope) {
		t.Fatal("begin() returned false")
	}
	ctx := testCtx()
	if _, err := callAuditor.beforeModel(ctx, &model.LLMRequest{Model: "test-model"}); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	if got := len(recorder.eventsOfType(audit.EventLLMCallStarted)); got != 1 {
		t.Fatalf("started events = %d, want 1", got)
	}
	response := &model.LLMResponse{ModelVersion: "test-model"}
	if _, err := callAuditor.afterModel(ctx, response, nil); err != nil {
		t.Fatalf("afterModel() error = %v", err)
	}
	if got := len(recorder.eventsOfType(audit.EventLLMCallCompleted)); got != 1 {
		t.Fatalf("completed events = %d, want 1", got)
	}
	last := recorder.lastEvent()
	if last.Status != audit.StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", last.Status)
	}
	callAuditor.end(ledgerHandle("session_1", scope), nil)
	if got := len(recorder.eventsOfType(audit.EventLLMCallFailed)); got != 0 {
		t.Fatalf("failed events after clean end = %d, want 0", got)
	}
}

func TestLedgerAggregatesRunUsage(t *testing.T) {
	callAuditor, _ := newTestLedger(t)
	scope := newTestScope()
	if !callAuditor.begin(ledgerHandle("session_1", scope), scope) {
		t.Fatal("begin() returned false")
	}
	ctx := testCtx()
	responses := []*model.LLMResponse{
		{ModelVersion: "test-model", UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 10, CandidatesTokenCount: 20, ThoughtsTokenCount: 1,
		}},
		{ModelVersion: "test-model", UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 5, CandidatesTokenCount: 7, ThoughtsTokenCount: 2,
		}},
	}
	for _, response := range responses {
		if _, err := callAuditor.beforeModel(ctx, &model.LLMRequest{Model: "test-model"}); err != nil {
			t.Fatalf("beforeModel() error = %v", err)
		}
		if _, err := callAuditor.afterModel(ctx, response, nil); err != nil {
			t.Fatalf("afterModel() error = %v", err)
		}
	}
	usage := scope.usage()
	if usage.LLMCalls != 2 || usage.PromptTokens != 15 ||
		usage.CompletionTokens != 27 || usage.ThoughtTokens != 3 {
		t.Fatalf("aggregated usage = %+v", usage)
	}
}

func TestBeforeModelRejectsMissingAuditScope(t *testing.T) {
	callAuditor, _ := newTestLedger(t)
	_, err := callAuditor.beforeModel(
		&fakeAgentContext{sessionID: "missing", invocationID: "inv_missing", agentName: "coordinator"},
		&model.LLMRequest{Model: "test-model"},
	)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeAuditScopeMissing {
		t.Fatalf("beforeModel() error = %v, want %s", err, domain.CodeAuditScopeMissing)
	}
}

func TestLedgerRetainsActiveCallWhenTerminalAuditWriteFails(t *testing.T) {
	recorder := &failOnceAudit{eventType: audit.EventLLMCallCompleted}
	callAuditor := newLLMCallAuditor(recorder, nil, "test", NewTestDeployment(t), fixedLedgerNow)
	scope := newTestScope()
	if !callAuditor.begin(ledgerHandle("session_1", scope), scope) {
		t.Fatal("begin() returned false")
	}
	ctx := testCtx()
	if _, err := callAuditor.beforeModel(ctx, &model.LLMRequest{Model: "test-model"}); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	auditErr := errors.New("terminal audit unavailable")
	if _, err := callAuditor.afterModel(ctx, &model.LLMResponse{}, nil); err == nil {
		t.Fatal("afterModel() unexpectedly survived terminal audit failure")
	}
	scope.mu.Lock()
	active := len(scope.activeCalls)
	scope.mu.Unlock()
	if active != 1 {
		t.Fatalf("active calls after terminal audit failure = %d, want 1", active)
	}
	if err := callAuditor.reconcile(scope, auditErr); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	scope.mu.Lock()
	active = len(scope.activeCalls)
	scope.mu.Unlock()
	if active != 0 {
		t.Fatalf("active calls after reconciliation = %d, want 0", active)
	}
	if got := len(recorder.eventsOfType(audit.EventLLMCallFailed)); got != 1 {
		t.Fatalf("terminal failed events = %d, want 1", got)
	}
}

func TestLedgerLifecycleFailurePath(t *testing.T) {
	callAuditor, recorder := newTestLedger(t)
	scope := newTestScope()
	callAuditor.begin(ledgerHandle("session_1", scope), scope)
	ctx := testCtx()
	if _, err := callAuditor.beforeModel(ctx, &model.LLMRequest{Model: "test-model"}); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	runErr := domain.NewError(domain.CodeRuntimeTimeout, "LLM timed out", nil)
	_, modelErr := callAuditor.onModelError(ctx, &model.LLMRequest{Model: "test-model"}, runErr)
	if modelErr == nil {
		t.Fatal("onModelError() should propagate the request error")
	}
	if got := len(recorder.eventsOfType(audit.EventLLMCallFailed)); got != 1 {
		t.Fatalf("failed events = %d, want 1", got)
	}
	last := recorder.lastEvent()
	if last.ErrorCode != domain.CodeRuntimeTimeout {
		t.Fatalf("error_code = %s, want %s", last.ErrorCode, domain.CodeRuntimeTimeout)
	}
	callAuditor.end(ledgerHandle("session_1", scope), nil)
	if got := len(recorder.eventsOfType(audit.EventLLMCallFailed)); got != 1 {
		t.Fatalf("failed events after end = %d, want 1", got)
	}
}

func TestLedgerInterruptedRunFailsActiveCalls(t *testing.T) {
	callAuditor, recorder := newTestLedger(t)
	scope := newTestScope()
	callAuditor.begin(ledgerHandle("session_1", scope), scope)
	ctx := testCtx()
	if _, err := callAuditor.beforeModel(ctx, &model.LLMRequest{Model: "test-model"}); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	callAuditor.end(ledgerHandle("session_1", scope), nil)
	if got := len(recorder.eventsOfType(audit.EventLLMCallFailed)); got != 1 {
		t.Fatalf("failed events = %d, want 1", got)
	}
	last := recorder.lastEvent()
	if last.Status != audit.StatusFailed {
		t.Fatalf("last event = %+v, want failed", last)
	}
	if last.DefinitionName == "" || last.DefinitionDigest == "" {
		t.Fatalf("interrupted LLM audit lacks agentDeployment provenance: %+v", last)
	}
}

func TestLedgerOnModelErrorWithoutStartedRecordsStartedAndFailed(t *testing.T) {
	callAuditor, recorder := newTestLedger(t)
	scope := newTestScope()
	if !callAuditor.begin(ledgerHandle("session_1", scope), scope) {
		t.Fatal("begin() returned false")
	}
	ctx := testCtx()
	runErr := domain.NewError(domain.CodeRuntimeTimeout, "LLM timed out", nil)
	if _, err := callAuditor.onModelError(ctx, &model.LLMRequest{Model: "test-model"}, runErr); err == nil {
		t.Fatal("onModelError() should propagate the request error")
	}
	started := recorder.eventsOfType(audit.EventLLMCallStarted)
	failed := recorder.eventsOfType(audit.EventLLMCallFailed)
	if len(started) != 1 || len(failed) != 1 {
		t.Fatalf("started/failed events = %d/%d, want 1/1", len(started), len(failed))
	}
	startedBase := strings.TrimSuffix(started[0].ID, ":"+string(audit.EventLLMCallStarted))
	failedBase := strings.TrimSuffix(failed[0].ID, ":"+string(audit.EventLLMCallFailed))
	if startedBase != failedBase {
		t.Fatalf("started base %q != failed base %q", startedBase, failedBase)
	}
	if started[0].Status != audit.StatusRunning || failed[0].Status != audit.StatusFailed {
		t.Fatalf("statuses = %s/%s, want running/failed", started[0].Status, failed[0].Status)
	}
}

func TestLedgerDuplicateBeginRejected(t *testing.T) {
	callAuditor, _ := newTestLedger(t)
	firstScope := newTestScope()
	if !callAuditor.begin(ledgerHandle("session_1", firstScope), firstScope) {
		t.Fatal("first begin should succeed")
	}
	if callAuditor.begin(ledgerHandle("session_1", newTestScope()), newTestScope()) {
		t.Fatal("second begin should fail")
	}
	callAuditor.end(ledgerHandle("session_1", firstScope), nil)
	secondScope := newTestScope()
	if !callAuditor.begin(ledgerHandle("session_1", secondScope), secondScope) {
		t.Fatal("begin after end should succeed")
	}
}

func TestLedgerConcurrentCallbacksDoNotRace(t *testing.T) {
	callAuditor, recorder := newTestLedger(t)
	scope := newTestScope()
	callAuditor.begin(ledgerHandle("session_1", scope), scope)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			invocationCtx := &fakeAgentContext{
				sessionID:    "session_1",
				invocationID: fmt.Sprintf("inv_%d", i),
				agentName:    "coordinator",
			}
			_, _ = callAuditor.beforeModel(invocationCtx, &model.LLMRequest{Model: "test-model"})
			_, _ = callAuditor.afterModel(invocationCtx, &model.LLMResponse{ModelVersion: "test-model"}, nil)
		}()
	}
	wg.Wait()
	callAuditor.end(ledgerHandle("session_1", scope), nil)
	if got := len(recorder.eventsOfType(audit.EventLLMCallStarted)); got != 50 {
		t.Fatalf("started events = %d, want 50", got)
	}
	if got := len(recorder.events); got != 100 {
		t.Fatalf("audit events = %d, want 100 started/completed-or-failed facts", got)
	}
}

func TestLedgerResolvesConcurrentAttemptsInOneSession(t *testing.T) {
	callAuditor, recorder := newTestLedger(t)
	const sessionID = "context_1"

	scopeOne := newTestScope()
	scopeOne.identity.RunID = "attempt_one"
	scopeOne.attemptKey = string(scopeOne.identity.RunID)
	handleOne := ledgerHandle(sessionID, scopeOne)
	if !callAuditor.begin(handleOne, scopeOne) {
		t.Fatal("begin attempt one should succeed")
	}

	scopeTwo := newTestScope()
	scopeTwo.identity.RunID = "attempt_two"
	scopeTwo.attemptKey = string(scopeTwo.identity.RunID)
	handleTwo := ledgerHandle(sessionID, scopeTwo)
	if !callAuditor.begin(handleTwo, scopeTwo) {
		t.Fatal("begin attempt two should succeed for the same ADK session")
	}

	// A2A injects the attempt into the executor context before ADK invokes the
	// model. This mirrors the callback path and ensures the legacy
	// single-attempt session fallback is not being used by accident.
	ctx := &fakeAgentContext{
		sessionID:    sessionID,
		invocationID: "inv_attempt_two",
		agentName:    "coordinator",
		traceContext: WithAuditAttempt(context.Background(), string(scopeTwo.identity.RunID)),
	}
	if _, err := callAuditor.beforeModel(ctx, &model.LLMRequest{Model: "test-model"}); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	if _, err := callAuditor.afterModel(ctx, &model.LLMResponse{ModelVersion: "test-model"}, nil); err != nil {
		t.Fatalf("afterModel() error = %v", err)
	}

	started := recorder.eventsOfType(audit.EventLLMCallStarted)
	completed := recorder.eventsOfType(audit.EventLLMCallCompleted)
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("attempt-two events = %d/%d, want 1/1", len(started), len(completed))
	}
	if string(started[0].RunID) != string(scopeTwo.identity.RunID) ||
		string(completed[0].RunID) != string(scopeTwo.identity.RunID) {
		t.Fatalf("attempt-two events crossed attempts: %#v / %#v", started[0], completed[0])
	}

	callAuditor.end(handleOne, nil)
	callAuditor.end(handleTwo, nil)
	if _, ok := callAuditor.scope(sessionID); ok {
		t.Fatalf("attempt registry leaked context %q", sessionID)
	}
	if _, ok := callAuditor.scope(string(handleOne.AttemptID)); ok {
		t.Fatalf("attempt registry leaked attempt %q", handleOne.AttemptID)
	}
	if _, ok := callAuditor.scope(string(handleTwo.AttemptID)); ok {
		t.Fatalf("attempt registry leaked attempt %q", handleTwo.AttemptID)
	}
}

func TestLedgerUsesInvokingAgentPromptProvenance(t *testing.T) {
	agentDeployment := NewTestDeployment(t)
	root := agentDeployment.Spec.Agents[0]
	root.SubAgents = []deploymentpkg.AgentReference{{Name: "worker"}}
	worker := root
	worker.Name = "worker"
	worker.InstructionDigest = "sha256:worker-instruction"
	worker.SubAgents = nil
	agentDeployment.Spec.Agents = append(agentDeployment.Spec.Agents, worker)

	events := &recordingAudit{}
	callAuditor := newLLMCallAuditor(events, nil, "test-provider", agentDeployment, fixedLedgerNow)
	scope := newTestScope()
	callAuditor.begin(ledgerHandle("session_worker", scope), scope)
	ctx := &fakeAgentContext{
		sessionID:    "session_worker",
		invocationID: "inv_worker",
		agentName:    "worker",
	}
	if _, err := callAuditor.beforeModel(ctx, &model.LLMRequest{Model: "test-model"}); err != nil {
		t.Fatalf("beforeModel() error = %v", err)
	}
	started := events.eventsOfType(audit.EventLLMCallStarted)
	if len(started) != 1 {
		t.Fatalf("started events = %d, want 1", len(started))
	}
	expectedDigest := agentDeployment.Spec.Agents[len(agentDeployment.Spec.Agents)-1].Digest
	if started[0].AgentName != "worker" || started[0].AgentDefinitionDigest != expectedDigest {
		t.Fatalf("worker provenance = %#v", started[0])
	}
	if got := started[0].Payload["prompt_version"]; got != "sha256:worker-instruction" {
		t.Fatalf("prompt_version = %#v, want worker instruction digest", got)
	}
	if started[0].DefinitionName == "" || started[0].DefinitionDigest == "" {
		t.Fatalf("LLM audit lacks agentDeployment provenance: %#v", started[0])
	}
}

// TestLLMUsageFromResponse maps genai usage metadata into token accounting,
// including the empty case.
func TestLLMUsageFromResponse(t *testing.T) {
	if got := llmUsageFromResponse(nil); got != (execution.LLMTokenUsage{}) {
		t.Fatalf("llmUsageFromResponse(nil) = %+v, want zero", got)
	}
	got := llmUsageFromResponse(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     10,
		CandidatesTokenCount: 2,
		ThoughtsTokenCount:   1,
		TotalTokenCount:      13,
	})
	want := execution.LLMTokenUsage{PromptTokens: 10, CompletionTokens: 2, ThoughtTokens: 1, TotalTokens: 13}
	if got != want {
		t.Fatalf("llmUsageFromResponse() = %+v, want %+v", got, want)
	}
}
