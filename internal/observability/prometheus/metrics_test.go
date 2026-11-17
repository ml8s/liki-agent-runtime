package prometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ml8s/agentgo/internal/execution"
)

func TestMetricsExposeStandardRuntimeDimensions(t *testing.T) {
	metrics := New()
	metrics.ObserveHTTPRequest("ag_ui", "run", "200", 25*time.Millisecond)
	metrics.StreamOpened("ag_ui")
	metrics.ObserveLLMCall("gpt-test", "completed", execution.LLMTokenUsage{
		PromptTokens: 10, CompletionTokens: 2, ThoughtTokens: 3, TotalTokens: 15,
	})
	metrics.ObserveLLMCallLatency("gpt-test", "completed", 250*time.Millisecond)
	metrics.ObserveToolCall("coordinator", "tool_a", "succeeded", 25*time.Millisecond)
	metrics.ObserveAgentDelegation("coordinator", "worker", "succeeded", 50*time.Millisecond)
	metrics.ObserveAuditAppend("ok", time.Millisecond)
	metrics.ObserveMCPCatalog("mcp-a", "hit")
	metrics.ObserveAuditAppendBatch(18)
	metrics.ObserveAuditQueueDepth(0)
	metrics.ObserveRunRejection("busy")
	metrics.ObserveRunTerminalReconciliation("succeeded", 25*time.Millisecond)
	metrics.ObservePendingTerminalRuns(1)
	metrics.ObserveRunReservation("reserved")
	metrics.SetDependencyReady("mcp_a", true)

	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, name := range []string{
		"agentgo_protocol_requests_total",
		"agentgo_protocol_request_duration_seconds",
		"agentgo_protocol_active_streams",
		"agentgo_llm_calls_total",
		"agentgo_llm_tokens_total",
		"agentgo_llm_call_duration_seconds",
		"agentgo_tool_calls_total",
		"agentgo_tool_duration_seconds",
		"agentgo_delegations_total",
		"agentgo_delegation_duration_seconds",
		"agentgo_dependency_up",
		"agentgo_audit_appends_total",
		"agentgo_audit_append_duration_seconds",
		"agentgo_mcp_catalog_requests_total",
		"agentgo_audit_batch_events",
		"agentgo_audit_queue_depth",
		"agentgo_run_rejections_total",
		"agentgo_run_terminal_reconciliation_total",
		"agentgo_run_terminal_reconciliation_duration_seconds",
		"agentgo_pending_terminal_runs",
		"agentgo_run_reservations_total",
	} {
		if !strings.Contains(response.Body.String(), name) {
			t.Fatalf("metrics body missing %q", name)
		}
	}
	if !strings.Contains(response.Body.String(), `agentgo_llm_tokens_total{model="gpt-test",type="thought"} 3`) {
		t.Fatalf("thought token usage was not observed: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `agentgo_tool_calls_total{agent="coordinator",status="succeeded",tool="tool_a"} 1`) {
		t.Fatalf("tool call was not observed: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `agentgo_delegations_total{caller="coordinator",status="succeeded",target="worker"} 1`) {
		t.Fatalf("delegation was not observed: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `agentgo_run_terminal_reconciliation_total{status="succeeded"} 1`) {
		t.Fatalf("terminal reconciliation was not observed: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `agentgo_pending_terminal_runs 1`) {
		t.Fatalf("pending terminal gauge was not observed: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `agentgo_run_reservations_total{result="reserved"} 1`) {
		t.Fatalf("run reservation was not observed: %s", response.Body.String())
	}
}
