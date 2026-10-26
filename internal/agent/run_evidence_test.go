package agent

// Δ2 契约钉子：终态证据事务中补偿失败（拒收）时，因果错误编码进
// run.failed 终态事件并成功持久化，EndAuditRun 返回 nil——残差不再作为
// 返回值触发无意义的执行方重试。证据（失败原因）在事件内，不在返回值。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
)

// scriptedAuditEvents 按事件类型注入记录失败，其余照常记录。
type scriptedAuditEvents struct {
	mu      sync.Mutex
	events  []*audit.Event
	failFor map[audit.EventType]bool
}

func (r *scriptedAuditEvents) Record(_ context.Context, event *audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failFor[event.Type] {
		// 与真实 repository 一致：以 coded domain error 拒收。
		return audit.NewError(audit.CodeAuditAppendFailed,
			fmt.Sprintf("record rejected for %s", event.Type), nil)
	}
	r.events = append(r.events, event)
	return nil
}

func (r *scriptedAuditEvents) all() []*audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*audit.Event, len(r.events))
	copy(out, r.events)
	return out
}

func (r *scriptedAuditEvents) eventsOfType(eventType audit.EventType) []*audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	matched := make([]*audit.Event, 0)
	for _, event := range r.events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

func TestCloseRunEvidenceEncodesCompensationFailureAndPersistsTerminal(t *testing.T) {
	events := &scriptedAuditEvents{failFor: map[audit.EventType]bool{
		audit.EventDelegationFailed: true, // 补偿事件被拒收（模拟持久化故障）
	}}
	deployment := NewTestDeployment(t)
	// 双代理拓扑：coordinator 经 task 工具委派 worker（与 assembly E2E 同构）。
	deployment.Spec.Agents[0].Name = "coordinator"
	deployment.Spec.Agents[0].SubAgents = []AgentReference{{Name: "worker"}}
	deployment.Spec.Agents = append(deployment.Spec.Agents, ResolvedAgentDefinition{
		AgentDefinition: AgentDefinition{
			Name:        "worker",
			Version:     "1.0.0",
			Description: "generic worker",
			Mode:        AgentModeTask,
			SubAgents:   []AgentReference{},
			Instruction: deployment.Spec.Agents[0].Instruction,
			Output:      deployment.Spec.Agents[0].Output,
			Tools:       ToolAllowlist{Allow: map[string][]string{"test": {"test_tool"}}},
		},
		OutputSchema:    deployment.Spec.Agents[0].OutputSchema,
		ResolvedOutput:  deployment.Spec.Agents[0].ResolvedOutput,
		RawOutputSchema: deployment.Spec.Agents[0].RawOutputSchema,
	})
	if err := deployment.validate(); err != nil {
		t.Fatalf("validate deployment: %v", err)
	}
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	runtime, err := NewRuntime(Config{
		Model:           "test-model",
		ModelAPIKey:     "test-key",
		AgentDeployment: deployment,
		Provider:        "test-provider",
		AuditRecorder:   events,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	scope := RunAuditIdentity{RunID: "run_e1", ThreadID: "thread_e1", UserID: "user_e1"}
	sessionID := runSessionID(scope.RunID)
	ctx := context.Background()
	if err := runtime.BeginAuditRun(ctx, sessionID, scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	// 制造未决委派（started 已持久化，补偿在终态时发生）。
	if _, err := runtime.delegationAuditor.BeforeAgent(&fakeAgentContext{
		sessionID:    sessionID,
		agentName:    "worker",
		invocationID: "inv_e1",
	}); err != nil {
		t.Fatalf("BeforeAgent() error = %v", err)
	}

	// 契约钉子：补偿失败已编码进终态事件并成功持久化 → 返回 nil。
	if err := runtime.EndAuditRun(ctx, sessionID, nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v；补偿残差应编码进终态事件而非返回", err)
	}

	failed := events.eventsOfType(audit.EventRunFailed)
	if len(failed) != 1 {
		for _, e := range events.all() {
			t.Logf("recorded: %s status=%s err=%q", e.Type, e.Status, e.ErrorMessage)
		}
		t.Fatalf("run.failed events = %d, want 1", len(failed))
	}
	if !strings.Contains(failed[0].ErrorMessage, "record rejected for agent.delegation.failed") {
		t.Fatalf("failure cause = %q, want the rejected delegation compensation record", failed[0].ErrorMessage)
	}
	if got := len(events.eventsOfType(audit.EventDelegationStarted)); got != 1 {
		t.Fatalf("delegation.started events = %d, want 1", got)
	}
	// 被拒收的补偿事件不得出现在证据里（拒收即未持久化）。
	if got := len(events.eventsOfType(audit.EventDelegationFailed)); got != 0 {
		t.Fatalf("delegation.failed events = %d, want 0", got)
	}
}
