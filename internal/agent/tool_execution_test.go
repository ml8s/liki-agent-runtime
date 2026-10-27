package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type stubMetrics struct {
	calls   []string
	catalog []string
}

func (m *stubMetrics) ObserveLLMCall(string, string, domain.LLMTokenUsage) {}
func (m *stubMetrics) ObserveLLMCallLatency(string, string, time.Duration) {}

func (m *stubMetrics) ObserveAgentDelegation(caller, target, status string, _ time.Duration) {
	m.calls = append(m.calls, caller+"/"+target+"/"+status)
}

func (m *stubMetrics) ObserveToolCall(agent, tool, status string, _ time.Duration) {
	m.calls = append(m.calls, agent+"/"+tool+"/"+status)
}

func (m *stubMetrics) ObserveMCPCatalog(server, outcome string) {
	m.catalog = append(m.catalog, server+"/"+outcome)
}

func (m *stubMetrics) ObserveRunRejection(reason string) {
	m.calls = append(m.calls, "run_rejection/"+reason)
}

type fakeTool string

func (t fakeTool) Name() string        { return string(t) }
func (t fakeTool) Description() string { return "generic test tool" }
func (t fakeTool) IsLongRunning() bool { return false }

func newToolAuditor(t *testing.T) (*ToolExecutionAuditor, *recordingAudit, *stubMetrics, *fakeAgentContext) {
	t.Helper()
	agentDeployment := NewTestDeployment(t)
	events := &recordingAudit{}
	metrics := &stubMetrics{}
	ledger := newLLMCallAuditor(events, metrics, "test-provider", agentDeployment, func() time.Time {
		return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	})
	auditor := newToolExecutionAuditor(
		events,
		metrics,
		func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) },
		"test-provider",
		agentDeployment,
		ledger.auditScope,
	)
	scope := newTestScope()
	ledger.begin(ledgerHandle("session_1", scope), scope)

	traceProvider := sdktrace.NewTracerProvider()
	tracer := traceProvider.Tracer("tool-audit-test")
	traceCtx, span := tracer.Start(context.Background(), "tool audit test")
	t.Cleanup(func() {
		_ = traceProvider.Shutdown(context.Background())
		span.End()
	})

	ctx := &fakeAgentContext{
		sessionID:      "session_1",
		invocationID:   "inv_1",
		agentName:      "main",
		functionCallID: "call_1",
		traceContext:   traceCtx,
	}
	return auditor, events, metrics, ctx
}

func TestToolAuditorRecordsSuccessfulCall(t *testing.T) {
	auditor, events, metrics, ctx := newToolAuditor(t)
	args := map[string]any{"question": "career"}
	replacement, err := auditor.BeforeTool(ctx, fakeTool("test_tool"), args)
	if replacement != nil || err != nil {
		t.Fatalf("BeforeTool() = %#v, %v, want nil replacement", replacement, err)
	}
	if _, err := auditor.AfterTool(ctx, fakeTool("test_tool"), args, map[string]any{"answer": "ok"}, nil); err != nil {
		t.Fatalf("AfterTool() error = %v", err)
	}
	started := events.eventsOfType(audit.EventToolCallStarted)
	completed := events.eventsOfType(audit.EventToolCallCompleted)
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("tool audit events = %d started / %d completed, want 1/1", len(started), len(completed))
	}
	if started[0].ToolCallID != "call_1" || started[0].Status != audit.StatusRunning {
		t.Fatalf("started event = %+v", started[0])
	}
	if started[0].TraceID == "" || started[0].SpanID == "" {
		t.Fatal("started audit event lacks trace correlation")
	}
	if completed[0].Status != audit.StatusSucceeded || completed[0].DurationMS != 0 {
		t.Fatalf("completed event = %+v", completed[0])
	}
	if got := completed[0].Payload["input_digest"]; !strings.HasPrefix(got.(string), "sha256:") {
		t.Fatalf("input digest = %#v, want sha256 digest", got)
	}
	if got := completed[0].Payload["output_bytes"]; got.(int64) == 0 {
		t.Fatalf("output bytes = %#v, want non-zero", got)
	}
	if completed[0].Payload["mcp_server"] != "test" {
		t.Fatalf("MCP server provenance = %#v, want test", completed[0].Payload["mcp_server"])
	}
	if len(metrics.calls) != 1 || metrics.calls[0] != "main/test_tool/succeeded" {
		t.Fatalf("tool metrics = %v", metrics.calls)
	}
}

func TestToolAuditorRejectsToolOutsideAgentAllowlist(t *testing.T) {
	auditor, events, _, ctx := newToolAuditor(t)
	_, err := auditor.BeforeTool(ctx, fakeTool("unauthorized_tool"), nil)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeToolCallInvalid {
		t.Fatalf("BeforeTool() error = %v, want %s", err, domain.CodeToolCallInvalid)
	}
	if got := len(events.eventsOfType(audit.EventToolCallStarted)); got != 0 {
		t.Fatalf("unauthorized tool audit events = %d, want 0", got)
	}
}

func TestToolAuditorSkipsBuiltInADKTransferTool(t *testing.T) {
	auditor, events, metrics, ctx := newToolAuditor(t)
	args := map[string]any{"agent_name": "sub"}
	replacement, err := auditor.BeforeTool(ctx, fakeTool(adkTransferToolName), args)
	if err != nil || replacement != nil {
		t.Fatalf("BeforeTool() = %#v, %v, want no replacement", replacement, err)
	}
	result := map[string]any{"status": "transferred"}
	got, err := auditor.AfterTool(ctx, fakeTool(adkTransferToolName), args, result, nil)
	if err != nil || !reflect.DeepEqual(got, result) {
		t.Fatalf("AfterTool() = %#v, %v, want original delegation result", got, err)
	}
	if len(events.events) != 0 {
		t.Fatalf("built-in delegation emitted MCP tool audit events: %+v", events.events)
	}
	if len(metrics.calls) != 0 {
		t.Fatalf("built-in delegation emitted MCP tool metrics: %v", metrics.calls)
	}
}

func TestToolAuditorRecordsFailedCall(t *testing.T) {
	auditor, events, metrics, ctx := newToolAuditor(t)
	if _, err := auditor.BeforeTool(ctx, fakeTool("test_tool"), nil); err != nil {
		t.Fatalf("BeforeTool() error = %v", err)
	}
	toolErr := errors.New("tool failed: secret domain payload")
	result, auditErr := auditor.AfterTool(ctx, fakeTool("test_tool"), nil, nil, toolErr)
	if !errors.Is(auditErr, toolErr) || result != nil {
		t.Fatalf("AfterTool() = %#v, %v, want original error", result, auditErr)
	}
	failed := events.eventsOfType(audit.EventToolCallFailed)
	if len(failed) != 1 {
		t.Fatalf("failed events = %d, want 1", len(failed))
	}
	if failed[0].ErrorCode != domain.CodeToolExecutionFailed {
		t.Fatalf("error code = %s", failed[0].ErrorCode)
	}
	if strings.Contains(failed[0].ErrorMessage, "secret") {
		t.Fatalf("failed audit event leaked tool payload: %q", failed[0].ErrorMessage)
	}
	if failed[0].TraceID == "" || failed[0].SpanID == "" {
		t.Fatal("failed audit event lacks trace correlation")
	}
	if len(metrics.calls) != 1 || !strings.HasSuffix(metrics.calls[0], "/failed") {
		t.Fatalf("tool metrics = %v", metrics.calls)
	}
}

func TestToolAuditorFailsPendingWhenRunEnds(t *testing.T) {
	auditor, events, metrics, ctx := newToolAuditor(t)
	if _, err := auditor.BeforeTool(ctx, fakeTool("test_tool"), map[string]any{"input": "value"}); err != nil {
		t.Fatalf("BeforeTool() error = %v", err)
	}

	if err := auditor.FailPending(ctx, newTestScope()); err != nil {
		t.Fatalf("FailPending() error = %v", err)
	}
	if err := auditor.FailPending(ctx, newTestScope()); err != nil {
		t.Fatalf("second FailPending() error = %v", err)
	}

	failed := events.eventsOfType(audit.EventToolCallFailed)
	if len(failed) != 1 {
		t.Fatalf("failed events = %d, want 1", len(failed))
	}
	if failed[0].ErrorCode != domain.CodeToolExecutionInterrupted {
		t.Fatalf("error code = %s, want %s", failed[0].ErrorCode, domain.CodeToolExecutionInterrupted)
	}
	if failed[0].ErrorMessage != "tool execution interrupted by run end" {
		t.Fatalf("error message = %q, want sanitized terminal message", failed[0].ErrorMessage)
	}
	if failed[0].Payload["input_digest"].(string) == "" {
		t.Fatal("interrupted tool audit lacks input digest")
	}
	if failed[0].TraceID == "" || failed[0].SpanID == "" {
		t.Fatal("interrupted tool audit lacks trace correlation")
	}
	if len(metrics.calls) != 1 || !strings.HasSuffix(metrics.calls[0], "/failed") {
		t.Fatalf("tool metrics = %v", metrics.calls)
	}
}

func TestToolAuditorRetainsPendingWhenTerminalAuditWriteFails(t *testing.T) {
	events := &failOnceAudit{eventType: audit.EventToolCallCompleted}
	agentDeployment := NewTestDeployment(t)
	ledger := newLLMCallAuditor(events, nil, "test-provider", agentDeployment, fixedLedgerNow)
	auditor := newToolExecutionAuditor(
		events,
		nil,
		fixedLedgerNow,
		"test-provider",
		agentDeployment,
		ledger.auditScope,
	)
	scope := newTestScope()
	ledger.begin(ledgerHandle("session_1", scope), scope)
	ctx := &fakeAgentContext{
		sessionID:      "session_1",
		invocationID:   "inv_1",
		agentName:      "main",
		functionCallID: "call_1",
	}
	if _, err := auditor.BeforeTool(ctx, fakeTool("test_tool"), nil); err != nil {
		t.Fatalf("BeforeTool() error = %v", err)
	}
	if _, err := auditor.AfterTool(ctx, fakeTool("test_tool"), nil, map[string]any{"ok": true}, nil); err == nil {
		t.Fatal("AfterTool() unexpectedly survived terminal audit failure")
	}
	if err := auditor.FailPending(context.Background(), scope); err != nil {
		t.Fatalf("FailPending() reconciliation error = %v", err)
	}
	if got := len(events.eventsOfType(audit.EventToolCallFailed)); got != 1 {
		t.Fatalf("terminal failed events = %d, want 1", got)
	}
	if err := auditor.FailPending(context.Background(), scope); err != nil {
		t.Fatalf("second FailPending() error = %v", err)
	}
	if got := len(events.eventsOfType(audit.EventToolCallFailed)); got != 1 {
		t.Fatalf("terminal failed events after reconciliation = %d, want 1", got)
	}
}

// TestToolAuditorFailPendingRetainsRecordForRetry guards reconciliation:
// a transient append failure must not dequeue the pending record, so
// EndAuditRun's cleanup retry can still emit terminal evidence.
func TestToolAuditorFailPendingRetainsRecordForRetry(t *testing.T) {
	recorder := &failOnceAudit{eventType: audit.EventToolCallFailed}
	agentDeployment := NewTestDeployment(t)
	metrics := &stubMetrics{}
	ledger := newLLMCallAuditor(recorder, metrics, "test-provider", agentDeployment, fixedLedgerNow)
	auditor := newToolExecutionAuditor(recorder, nil, fixedLedgerNow, "test-provider", agentDeployment, ledger.auditScope)
	scope := newTestScope()
	ledger.begin(ledgerHandle("session_retry", scope), scope)
	ctx := &fakeAgentContext{
		sessionID:      "session_retry",
		invocationID:   "inv_retry",
		agentName:      "main",
		functionCallID: "call_retry",
	}
	if _, err := auditor.BeforeTool(ctx, fakeTool("test_tool"), map[string]any{"input": "x"}); err != nil {
		t.Fatalf("BeforeTool() error = %v", err)
	}
	// First reconciliation hits a transient write failure; the record must stay
	// pending instead of being dequeued into the void.
	if err := auditor.FailPending(context.Background(), newTestScope()); err == nil {
		t.Fatal("FailPending() unexpectedly survived a terminal append failure")
	}
	if got := len(recorder.eventsOfType(audit.EventToolCallFailed)); got != 0 {
		t.Fatalf("failed events after failed attempt = %d, want 0", got)
	}
	// A retry (cleanup callback) now emits the terminal evidence.
	if err := auditor.FailPending(context.Background(), newTestScope()); err != nil {
		t.Fatalf("retry FailPending() error = %v", err)
	}
	if got := len(recorder.eventsOfType(audit.EventToolCallFailed)); got != 1 {
		t.Fatalf("failed events after retry = %d, want 1", got)
	}
}
