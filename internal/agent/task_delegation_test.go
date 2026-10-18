package agent

import (
	"context"
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// taskDelegationDeployment builds the standard ADK agent-as-tool shape: a
// plain-text chat coordinator with one task-mode sub-agent. It is the same
// shape the natal expert uses for bazi/ziwei.
func taskDelegationDeployment(t *testing.T) *Deployment {
	t.Helper()
	deployment := plainTestDeployment(t)
	root := &deployment.Spec.Agents[0]
	root.Name = "coordinator"
	root.SubAgents = []AgentReference{{Name: "worker"}}
	worker := *root
	worker.Name = "worker"
	worker.Description = "generic task worker"
	worker.Mode = AgentModeTask
	worker.SubAgents = nil
	deployment.Spec.Agents = append(deployment.Spec.Agents, worker)
	if err := deployment.validate(); err != nil {
		t.Fatalf("validate task delegation deployment: %v", err)
	}
	return deployment
}

// taskDelegationLLM scripts one model invocation per turn: coordinator
// delegates to the task sub-agent, the task agent finishes via finish_task,
// and the coordinator synthesises the final answer from the delegated result.
func taskDelegationLLM() *turnBasedLLM {
	return &turnBasedLLM{turns: []*model.LLMResponse{
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "fc_worker_1", Name: "worker", Args: map[string]any{"request": "complete the task"},
		}}}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "fc_finish_1", Name: "finish_task", Args: map[string]any{"result": "worker summary"},
		}}}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "coordinator final answer"}}}},
	}}
}

func functionResponseOf(events []*session.Event, name string) map[string]any {
	for _, event := range events {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part == nil || part.FunctionResponse == nil || part.FunctionResponse.Name != name {
				continue
			}
			return part.FunctionResponse.Response
		}
	}
	return nil
}

func TestRuntimeRunsTaskDelegationEndToEnd(t *testing.T) {
	deployment := taskDelegationDeployment(t)
	events := &recordingAudit{}
	runtime := newTestRuntime(t, deployment, events, taskDelegationLLM())

	var observed []*session.Event
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_task_delegation", ThreadID: "thread_task_delegation", UserID: "user_1",
		UserMessage: "delegate this",
	}, func(event *session.Event) error {
		observed = append(observed, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "coordinator final answer" {
		t.Fatalf("Text = %q, want coordinator final answer", result.Text)
	}
	// The final answer must be authored by the coordinator, not by the task
	// sub-agent leaking its scripted turn into the run result.
	finalAuthor := ""
	for _, event := range observed {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.Text == "coordinator final answer" {
				finalAuthor = event.Author
			}
		}
	}
	if finalAuthor != "coordinator" {
		t.Fatalf("final answer author = %q, want coordinator", finalAuthor)
	}
	// The task agent's finish_task output must be returned to the coordinator
	// as the synthesised function response of the task tool call.
	if response := functionResponseOf(observed, "worker"); response["result"] != "worker summary" {
		t.Fatalf("task tool response = %#v, want worker summary", response)
	}
	// finish_task must execute through the standard tool pipeline.
	if response := functionResponseOf(observed, "finish_task"); response["result"] != "Task completed." {
		t.Fatalf("finish_task response = %#v, want success", response)
	}
	if got := len(events.eventsOfType(audit.EventDelegationStarted)); got != 1 {
		t.Fatalf("delegation started events = %d, want 1", got)
	}
	if got := len(events.eventsOfType(audit.EventDelegationCompleted)); got != 1 {
		t.Fatalf("delegation completed events = %d, want 1", got)
	}
	if got := len(events.eventsOfType(audit.EventDelegationFailed)); got != 0 {
		t.Fatalf("delegation failed events = %d, want 0", got)
	}
	// ADK's framework delegation tools (the task tool named after the
	// sub-agent and the task agent's finish_task) are not MCP tools; they must
	// not be audited as MCP tool calls, exactly like transfer_to_agent.
	for _, eventType := range []audit.EventType{
		audit.EventToolCallStarted,
		audit.EventToolCallCompleted,
		audit.EventToolCallFailed,
	} {
		for _, event := range events.eventsOfType(eventType) {
			if event.ToolName == "worker" || event.ToolName == "finish_task" {
				t.Fatalf("framework delegation tool %q produced %s evidence", event.ToolName, eventType)
			}
		}
	}
}
