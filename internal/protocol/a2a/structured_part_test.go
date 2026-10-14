package a2a

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/ml8s/liki-agent-runtime/internal/agent"
	"github.com/ml8s/liki-agent-runtime/internal/testagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// exposeAll treats every author as plain-text visible; exposeNone hides every
// author's model text. Both stand in for a deployment-derived resolver.
func exposeAll() func(string) bool { return func(string) bool { return true } }

func exposeNone() func(string) bool { return func(string) bool { return false } }

// exposeByDeployment resolves model-text visibility per author from the
// deployment, mirroring the resolver built by a2a.New.
func exposeByDeployment(deployment *agent.Deployment) func(string) bool {
	return func(author string) bool {
		definition, ok := deployment.Agent(author)
		return ok && agent.ExposesModelText(definition)
	}
}

// visibilityDeployment models a plain-text entrypoint delegating to a
// structured expert, the topology that previously leaked raw model JSON.
func visibilityDeployment() *agent.Deployment {
	return &agent.Deployment{
		Metadata: agent.DeploymentMetadata{Name: "t", Version: "1.0.0"},
		Spec: agent.DeploymentSpec{Agents: []agent.AgentDefinition{
			{Name: "main"},
			{Name: "worker", Output: agent.OutputDefinition{TextPointer: "/answer"}},
		}},
	}
}

func TestAgentPartPassesPlainTextThroughFrameworkMapping(t *testing.T) {
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "plain answer"}}},
		},
	}
	part, err := agentPart(event, &genai.Part{Text: "plain answer"}, exposeAll(), nil)
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	text, ok := part.Content.(a2a.Text)
	if !ok || text != "plain answer" {
		t.Fatalf("artifact text = %#v, want framework-mapped plain answer", part.Content)
	}
}

func TestAgentPartDropsPartialPayloadText(t *testing.T) {
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"par`}}},
		},
	}
	part, err := agentPart(event, &genai.Part{Text: `{"answer":"par`}, exposeNone(), structuredEntrypoint(t))
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	if part != nil {
		t.Fatalf("partial payload part = %+v, want nil", part)
	}
}

func TestAgentPartSubstitutesDeclaredPointer(t *testing.T) {
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"raw payload"}`}}},
		},
		Actions: session.EventActions{StateDelta: map[string]any{
			agent.StructuredOutputStateKey("main"): map[string]any{"answer": " 最终结论 "},
		}},
	}
	event.Author = "main"
	entrypoint := structuredEntrypoint(t)
	part, err := agentPart(event, &genai.Part{Text: `{"answer":"raw payload"}`}, exposeNone(), entrypoint)
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	data, ok := part.Content.(a2a.Data)
	if !ok {
		t.Fatalf("artifact content = %#v, want structured data", part.Content)
	}
	output, outputOK := data.Value.(map[string]any)
	if !outputOK || output["answer"] != " 最终结论 " {
		t.Fatalf("artifact data = %#v, want validated JSON output", data.Value)
	}
	if part.MediaType != "application/json" {
		t.Fatalf("artifact media type = %q", part.MediaType)
	}
	if len(part.Metadata) != 0 {
		t.Fatalf("artifact carries private metadata: %#v", part.Metadata)
	}
}

func TestAgentPartKeepsToolCallFacts(t *testing.T) {
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "test_tool"}},
			}},
		},
	}
	part, err := agentPart(event, &genai.Part{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "test_tool"}}, exposeNone(), structuredEntrypoint(t))
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	if part == nil {
		t.Fatal("tool call part was dropped, want framework default mapping")
	}
}

func TestAgentPartIgnoresNonEntrypointStructuredOutput(t *testing.T) {
	event := &session.Event{
		Author: "worker",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"worker payload"}`}}},
		},
		Actions: session.EventActions{StateDelta: map[string]any{
			agent.StructuredOutputStateKey("worker"): map[string]any{"answer": "worker answer"},
		}},
	}
	part, err := agentPart(event, &genai.Part{Text: `{"answer":"worker payload"}`}, exposeNone(), structuredEntrypoint(t))
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	if part != nil {
		t.Fatalf("worker structured part = %#v, want nil", part)
	}
}

func TestAgentPartSuppressesStructuredAuthorRawPayload(t *testing.T) {
	// A structured worker under a plain entrypoint must not leak its raw model
	// JSON on the A2A wire, matching the AG-UI projector's per-author rule.
	resolver := exposeByDeployment(visibilityDeployment())
	event := &session.Event{
		Author: "worker",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"internal","secret":true}`}}},
		},
	}
	part, err := agentPart(event, &genai.Part{Text: `{"answer":"internal","secret":true}`}, resolver, structuredEntrypoint(t))
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	if part != nil {
		t.Fatalf("structured worker raw payload = %#v, want suppressed", part)
	}
}

func TestAgentPartStreamsPlainAuthorText(t *testing.T) {
	// Per-author visibility: a plain author is streamed even when the
	// deployment's entrypoint is structured, consistent with the AG-UI
	// projector.
	resolver := exposeByDeployment(visibilityDeployment())
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "orchestrator note"}}},
		},
	}
	part, err := agentPart(event, &genai.Part{Text: "orchestrator note"}, resolver, structuredEntrypoint(t))
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	text, ok := part.Content.(a2a.Text)
	if !ok || text != "orchestrator note" {
		t.Fatalf("plain author artifact = %#v, want text", part.Content)
	}
}

func TestAgentPartHidesUnknownAuthorText(t *testing.T) {
	// Unknown or user-authored projections are not streamed, matching the
	// AG-UI projector's default.
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "user", Parts: []*genai.Part{{Text: "client echo"}}},
		},
	}
	part, err := agentPart(event, &genai.Part{Text: "client echo"}, exposeByDeployment(visibilityDeployment()), structuredEntrypoint(t))
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	if part != nil {
		t.Fatalf("unknown author part = %#v, want suppressed", part)
	}
}

func structuredEntrypoint(t *testing.T) *agent.AgentDefinition {
	t.Helper()
	entrypoint, err := testagent.Deployment(t).EntrypointDefinition()
	if err != nil {
		t.Fatal(err)
	}
	return entrypoint
}
