package prometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

func TestMetricsExposeStandardRuntimeDimensions(t *testing.T) {
	metrics := New()
	metrics.ObserveHTTPRequest("ag_ui", "run", "200", 25*time.Millisecond)
	metrics.StreamOpened("ag_ui")
	metrics.ObserveLLMCall("gpt-test", "completed", domain.LLMTokenUsage{
		PromptTokens: 10, CompletionTokens: 2, ThoughtTokens: 3, TotalTokens: 15,
	})
	metrics.ObserveToolCall("coordinator", "engine_tool_a", "succeeded", 25*time.Millisecond)
	metrics.ObserveAgentDelegation("coordinator", "worker", "succeeded", 50*time.Millisecond)
	metrics.SetDependencyReady("engine_mcp", true)

	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, name := range []string{
		"liki_agents_protocol_requests_total",
		"liki_agents_protocol_request_duration_seconds",
		"liki_agents_protocol_active_streams",
		"liki_agents_llm_calls_total",
		"liki_agents_llm_tokens_total",
		"liki_agents_tool_calls_total",
		"liki_agents_tool_duration_seconds",
		"liki_agents_delegations_total",
		"liki_agents_delegation_duration_seconds",
		"liki_agents_dependency_up",
	} {
		if !strings.Contains(response.Body.String(), name) {
			t.Fatalf("metrics body missing %q", name)
		}
	}
	if !strings.Contains(response.Body.String(), `liki_agents_llm_tokens_total{model="gpt-test",type="thought"} 3`) {
		t.Fatalf("thought token usage was not observed: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `liki_agents_tool_calls_total{agent="coordinator",status="succeeded",tool="engine_tool_a"} 1`) {
		t.Fatalf("tool call was not observed: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `liki_agents_delegations_total{caller="coordinator",status="succeeded",target="worker"} 1`) {
		t.Fatalf("delegation was not observed: %s", response.Body.String())
	}
}
