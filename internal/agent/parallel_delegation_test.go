package agent

import deploymentpkg "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"context"
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/audit"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// parallelDelegationDeployment builds a chat coordinator with one
// single-turn sub-agent. Single-turn delegation runs through the standard
// tool pipeline, so a model response that carries two function calls
// executes both sub-agent invocations concurrently.
func parallelDelegationDeployment(t *testing.T) *deploymentpkg.AgentDeployment {
	t.Helper()
	agentDeployment := plainTestDeployment(t)
	root := &agentDeployment.Spec.Agents[0]
	root.Name = "coordinator"
	root.SubAgents = []deploymentpkg.AgentReference{{Name: "worker"}}
	worker := *root
	worker.Name = "worker"
	worker.Description = "generic single-turn worker"
	worker.Mode = deploymentpkg.AgentModeSingleTurn
	worker.SubAgents = nil
	agentDeployment.Spec.Agents = append(agentDeployment.Spec.Agents, worker)
	if err := agentDeployment.Validate(); err != nil {
		t.Fatalf("validate parallel delegation agentDeployment: %v", err)
	}
	return agentDeployment
}

// TestRuntimeRunsParallelSingleTurnDelegation pins the parallel delegation
// guarantee: two single-turn calls to the SAME sub-agent in one model
// response execute concurrently, and the delegation audit stays correct —
// every invocation gets its own started/completed evidence with distinct
// event identities, despite sharing target name, session, and invocation.
func TestRuntimeRunsParallelSingleTurnDelegation(t *testing.T) {
	agentDeployment := parallelDelegationDeployment(t)
	events := &recordingAudit{}
	llm := &turnBasedLLM{turns: []*model.LLMResponse{
		// The coordinator emits two worker calls in one response.
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "fc_single_1", Name: "worker", Args: map[string]any{"request": "task A"}}},
			{FunctionCall: &genai.FunctionCall{ID: "fc_single_2", Name: "worker", Args: map[string]any{"request": "task B"}}},
		}}},
		// Each concurrent worker invocation consumes one text turn.
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "answer A"}}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "answer B"}}}},
		// The coordinator synthesises the final answer.
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "both done"}}}},
	}}
	runtime := newTestRuntime(t, agentDeployment, events, llm)

	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_parallel_delegation", ThreadID: "thread_parallel", UserID: "user_1",
		UserMessage: "fan out",
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "both done" {
		t.Fatalf("Run() text = %q, want coordinator final answer", result.Text)
	}

	started := events.eventsOfType(audit.EventDelegationStarted)
	completed := events.eventsOfType(audit.EventDelegationCompleted)
	if len(started) != 2 || len(completed) != 2 {
		t.Fatalf("delegation evidence: started=%d completed=%d, want 2 and 2", len(started), len(completed))
	}
	ids := make(map[string]struct{}, 4)
	for _, event := range append(append([]audit.Event{}, started...), completed...) {
		if _, dup := ids[event.ID]; dup {
			t.Fatalf("delegation event id %q repeated; parallel invocations are not distinct", event.ID)
		}
		ids[event.ID] = struct{}{}
	}
	for _, event := range completed {
		if event.Status != "succeeded" {
			t.Fatalf("completed delegation %q status = %q", event.ID, event.Status)
		}
	}
}
