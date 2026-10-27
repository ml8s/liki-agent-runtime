package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	deploymentpkg "github.com/ml8s/liki-agent-runtime/internal/deployment"

	"go.opentelemetry.io/otel/trace"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

func newDelegationAuditorForTest(t *testing.T, branch string) (*DelegationAuditor, *recordingAudit, *stubMetrics, *fakeAgentContext) {
	t.Helper()
	agentDeployment := NewTestDeployment(t)
	agentDeployment.Spec.Agents[0].Name = "coordinator"
	agentDeployment.Spec.Agents[0].SubAgents = []deploymentpkg.AgentReference{{Name: "worker"}}
	agentDeployment.Spec.Agents = append(agentDeployment.Spec.Agents, deploymentpkg.ResolvedAgentDefinition{AgentDefinition: deploymentpkg.AgentDefinition{
		Name:        "worker",
		Version:     "1.0.0",
		Description: "generic worker",
		Mode:        deploymentpkg.AgentModeTask,
		SubAgents:   []deploymentpkg.AgentReference{},
		Instruction: agentDeployment.Spec.Agents[0].Instruction,
		Output:      agentDeployment.Spec.Agents[0].Output,
		Tools:       deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {"test_tool"}}},
	}})
	if err := agentDeployment.Validate(); err != nil {
		t.Fatalf("validate agentDeployment: %v", err)
	}

	events := &recordingAudit{}
	metrics := &stubMetrics{}
	ledger := newLLMCallAuditor(events, metrics, "test-provider", agentDeployment, func() time.Time {
		return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	})
	auditor := newDelegationAuditor(
		events,
		metrics,
		func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) },
		agentDeployment,
		"coordinator",
		ledger.auditScope,
	)
	ledger.begin("session_1", newTestScope())
	ctx := &fakeAgentContext{
		sessionID:      "session_1",
		invocationID:   "inv_worker",
		agentName:      "worker",
		branch:         branch,
		functionCallID: "not-used",
		traceContext: func() context.Context {
			traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
			spanID, _ := trace.SpanIDFromHex("0102030405060708")
			spanContext := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID:    traceID,
				SpanID:     spanID,
				TraceFlags: trace.FlagsSampled,
			})
			return trace.ContextWithSpanContext(context.Background(), spanContext)
		}(),
	}
	sc := trace.SpanContextFromContext(ctx)
	t.Logf("fake trace context valid=%v trace=%s", sc.IsValid(), sc.TraceID())
	return auditor, events, metrics, ctx
}

func TestDelegationAuditorRecordsSuccessfulDelegation(t *testing.T) {
	auditor, events, metrics, ctx := newDelegationAuditorForTest(t, "coordinator.worker")
	if _, err := auditor.BeforeAgent(ctx); err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}
	if _, err := auditor.AfterAgent(ctx); err != nil {
		t.Fatalf("AfterAgent() error = %v", err)
	}
	started := events.eventsOfType(audit.EventDelegationStarted)
	completed := events.eventsOfType(audit.EventDelegationCompleted)
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("delegation events = %d started / %d completed, want 1/1", len(started), len(completed))
	}
	if started[0].CallerAgent != "coordinator" || started[0].TargetAgent != "worker" || started[0].DelegationDepth != 1 {
		t.Fatalf("started event = %+v", started[0])
	}
	if completed[0].Status != audit.StatusSucceeded || completed[0].DurationMS != 0 {
		t.Fatalf("completed event = %+v", completed[0])
	}
	t.Logf("started event=%+v", started[0])
	if started[0].TraceID == "" || started[0].SpanID == "" {
		t.Fatal("started delegation audit lacks trace correlation")
	}
	if len(metrics.calls) != 1 || metrics.calls[0] != "coordinator/worker/succeeded" {
		t.Fatalf("delegation metrics = %v", metrics.calls)
	}
}

func TestDelegationAuditorSkipsEntrypoint(t *testing.T) {
	auditor, events, _, ctx := newDelegationAuditorForTest(t, "coordinator")
	ctx.agentName = "coordinator"
	ctx.branch = "coordinator"
	if _, err := auditor.BeforeAgent(ctx); err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}
	if got := len(events.eventsOfType(audit.EventDelegationStarted)); got != 0 {
		t.Fatalf("entrypoint delegation events = %d, want 0", got)
	}
}

func TestDelegationAuditorFailsPending(t *testing.T) {
	auditor, events, metrics, ctx := newDelegationAuditorForTest(t, "coordinator.worker")
	ledger := newLLMCallAuditor(events, metrics, "test-provider", NewTestDeployment(t), func() time.Time {
		return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	})
	ledger.begin("session_1", newTestScope())
	if _, err := auditor.BeforeAgent(ctx); err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}
	if err := auditor.FailPending(context.Background(), newTestScope()); err != nil {
		t.Fatalf("FailPending() error = %v", err)
	}
	failed := events.eventsOfType(audit.EventDelegationFailed)
	if len(failed) != 1 {
		t.Fatalf("failed events = %d, want 1", len(failed))
	}
	if failed[0].ErrorCode != domain.CodeDelegationFailed {
		t.Fatalf("error code = %s", failed[0].ErrorCode)
	}
	if len(metrics.calls) != 1 || !strings.Contains(metrics.calls[0], "/failed") {
		t.Fatalf("delegation metrics = %v", metrics.calls)
	}
	if err := auditor.FailPending(context.Background(), newTestScope()); err != nil {
		t.Fatalf("second FailPending() error = %v", err)
	}
	if got := len(events.eventsOfType(audit.EventDelegationFailed)); got != 1 {
		t.Fatalf("failed events after second pass = %d, want 1", got)
	}
}

func TestDelegationAuditorRetainsPendingWhenTerminalAuditWriteFails(t *testing.T) {
	events := &failOnceAudit{eventType: audit.EventDelegationCompleted}
	agentDeployment := NewTestDeployment(t)
	agentDeployment.Spec.Agents[0].Name = "coordinator"
	agentDeployment.Spec.Agents[0].SubAgents = []deploymentpkg.AgentReference{{Name: "worker"}}
	agentDeployment.Spec.Agents = append(agentDeployment.Spec.Agents, deploymentpkg.ResolvedAgentDefinition{AgentDefinition: deploymentpkg.AgentDefinition{
		Name:        "worker",
		Version:     "1.0.0",
		Description: "worker",
		Mode:        deploymentpkg.AgentModeTask,
		Instruction: agentDeployment.Spec.Agents[0].Instruction,
		Tools:       deploymentpkg.ToolAllowlist{Allow: map[string][]string{}},
	}})
	if err := agentDeployment.Validate(); err != nil {
		t.Fatalf("validate agentDeployment: %v", err)
	}
	scope := newTestScope()
	ledger := newLLMCallAuditor(events, nil, "test-provider", agentDeployment, fixedLedgerNow)
	ledger.begin("session_1", scope)
	auditor := newDelegationAuditor(events, nil, fixedLedgerNow, agentDeployment, "coordinator", ledger.auditScope)
	ctx := &fakeAgentContext{
		sessionID:    "session_1",
		invocationID: "inv_worker",
		agentName:    "worker",
		branch:       "coordinator.worker",
	}
	if _, err := auditor.BeforeAgent(ctx); err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}
	if _, err := auditor.AfterAgent(ctx); err == nil {
		t.Fatal("AfterAgent() unexpectedly survived terminal audit failure")
	}
	if err := auditor.FailPending(context.Background(), scope); err != nil {
		t.Fatalf("FailPending() reconciliation error = %v", err)
	}
	if got := len(events.eventsOfType(audit.EventDelegationFailed)); got != 1 {
		t.Fatalf("terminal failed events = %d, want 1", got)
	}
}

var _ adkagent.BeforeAgentCallback = func(adkagent.Context) (*genai.Content, error) { return nil, nil }
