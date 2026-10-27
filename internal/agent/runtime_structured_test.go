package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/ml8s/agent-runtime/internal/audit"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// TestRuntimeRunProducesStructuredAnswerWithoutPayloadLeak is the keystone
// regression test for ADK structured output: the Runner clears
// Event.Output before yielding, so the runtime must read the parsed answer
// from session state, must never expose payload JSON to protocol observers,
// and must still persist the LLM audit lifecycle.
func TestRuntimeRunProducesStructuredAnswerWithoutPayloadLeak(t *testing.T) {
	payload := `{"answer":"八字显示压力而非确定性。","confidence":0.82}`
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: payload}}},
	}}})

	observed := make([]*session.Event, 0)
	result, err := runtime.Run(context.Background(), execution.RunRequest{
		RunID:       "run_1",
		ThreadID:    "thread_1",
		UserID:      "user_1",
		UserMessage: "看事业",
	}, func(event *session.Event) error {
		observed = append(observed, event)
		return nil
	})
	if err != nil {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("caused by: %v", cause)
		}
		t.Fatalf("Run() error = %v", err)
	}

	if result.Text != "八字显示压力而非确定性。" {
		t.Fatalf("Text = %q, want the parsed answer", result.Text)
	}
	if result.Definition.Name != "test-agent" || result.Definition.Version != "1.0.0" {
		t.Fatalf("definition ref = %+v", result.Definition)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	for index, event := range observed {
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.Text != "" && !part.Thought {
				t.Fatalf("observed[%d] leaks model payload text %q", index, part.Text)
			}
		}
	}
	for _, eventType := range []audit.EventType{
		audit.EventRunStarted,
		audit.EventLLMCallStarted,
		audit.EventLLMCallCompleted,
		audit.EventRunCompleted,
	} {
		if got := len(events.eventsOfType(eventType)); got != 1 {
			t.Fatalf("%s audit events = %d, want 1", eventType, got)
		}
	}
	if strings.Contains(result.Text, `\"answer\"`) {
		t.Fatal("answer still contains JSON payload markers")
	}
}

// TestStructuredAnswerConsistentAcrossProtocolSources proves the two protocol
// adapters derive the same user-facing answer from one run: AG-UI reads the
// validated session-state value (execution.RunResult.Text) while A2A reads the event
// StateDelta, and both must finalize through execution.ParseStructuredOutput.
func TestStructuredAnswerConsistentAcrossProtocolSources(t *testing.T) {
	agentDeployment := NewTestDeployment(t)
	entrypoint, err := agentDeployment.EntrypointDefinition()
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"answer":"一致结论","confidence":0.88}`
	events := &recordingAudit{}
	runtime := newTestRuntime(t, agentDeployment, events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: payload}}},
	}}})

	var stateDelta any
	var sawDelta bool
	result, err := runtime.Run(context.Background(), execution.RunRequest{
		RunID: "run_consistency", ThreadID: "thread_consistency", UserID: "user_1", UserMessage: "看事业",
	}, func(event *session.Event) error {
		if event != nil && event.Actions.StateDelta != nil {
			if value, ok := event.Actions.StateDelta[execution.StructuredOutputStateKey(entrypoint.Name)]; ok {
				stateDelta = value
				sawDelta = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawDelta {
		t.Fatal("structured entrypoint run did not project an observable StateDelta")
	}
	a2aOutput, err := execution.ParseStructuredOutput(stateDelta, entrypoint)
	if err != nil {
		t.Fatalf("execution.ParseStructuredOutput(StateDelta) error = %v", err)
	}
	if result.Text != a2aOutput.Text {
		t.Fatalf("AG-UI answer %q != A2A answer %q", result.Text, a2aOutput.Text)
	}
	if result.Text != "一致结论" {
		t.Fatalf("answer = %q, want the pointer-selected text", result.Text)
	}
}

func TestRuntimeBuildsStandardADKAgentGraph(t *testing.T) {
	agentDeployment := NewTestDeployment(t)
	// Convert the generic single-agent test agentDeployment into a two-Agent tree.
	agentDeployment.Spec.Agents[0].Name = "coordinator"
	agentDeployment.Spec.Agents[0].SubAgents = []deploymentpkg.AgentReference{{Name: "worker"}}
	agentDeployment.Spec.Agents = append(agentDeployment.Spec.Agents, deploymentpkg.ResolvedAgentDefinition{
		AgentDefinition: deploymentpkg.AgentDefinition{
			Name:        "worker",
			Version:     "1.0.0",
			Description: "generic worker agent",
			Mode:        deploymentpkg.AgentModeTask,
			Instruction: agentDeployment.Spec.Agents[0].Instruction,
			Output:      agentDeployment.Spec.Agents[0].Output,
			Tools:       deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {"test_tool"}}},
		},
		OutputSchema:    agentDeployment.Spec.Agents[0].OutputSchema,
		ResolvedOutput:  agentDeployment.Spec.Agents[0].ResolvedOutput,
		RawOutputSchema: agentDeployment.Spec.Agents[0].RawOutputSchema,
	})
	if err := agentDeployment.Validate(); err != nil {
		t.Fatalf("validate multi-agent agentDeployment: %v", err)
	}
	for index := range agentDeployment.Spec.Agents {
		agent := &agentDeployment.Spec.Agents[index]
		if err := agent.ValidateAgent(map[string]struct{}{"test": {}}); err != nil {
			t.Fatalf("validate agent %q: %v", agent.Name, err)
		}
	}

	events := &recordingAudit{}
	runtime := newTestRuntime(t, agentDeployment, events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"ok"}`}}},
	}}})
	if runtime.entrypoint.Name != "coordinator" {
		t.Fatalf("entrypoint = %q", runtime.entrypoint.Name)
	}
	worker := runtime.RootAgent().FindSubAgent("worker")
	if worker == nil {
		t.Fatal("worker sub-agent was not registered with ADK")
	}
	if worker.Name() != "worker" {
		t.Fatalf("worker name = %q", worker.Name())
	}
}
