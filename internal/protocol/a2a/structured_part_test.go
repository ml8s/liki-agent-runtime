package a2a

import (
	"fmt"
	"testing"

	deploymentpkg "github.com/ml8s/liki-agent-runtime/internal/deployment"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/ml8s/liki-agent-runtime/internal/execution"
	"github.com/ml8s/liki-agent-runtime/internal/testagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestStructuredMemoParsesOncePerEvent(t *testing.T) {
	entrypoint := structuredEntrypoint(t)
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"raw payload"}`}}},
		},
		Actions: session.EventActions{StateDelta: map[string]any{
			execution.StructuredOutputStateKey(entrypoint.Name): map[string]any{"answer": " 最终结论 "},
		}},
	}
	event.Author = entrypoint.Name
	parseCount := 0
	server := &Server{structured: structuredMemo{
		parse: func(value any, definition *deploymentpkg.ResolvedAgentDefinition) (execution.OutputResult, error) {
			parseCount++
			return execution.ParseStructuredOutput(value, definition)
		},
	}}

	for part := 0; part < 3; part++ {
		output, applies, err := server.structuredOutput(event, entrypoint)
		if !applies || err != nil {
			t.Fatalf("part %d structuredOutput() = applies=%v err=%v", part, applies, err)
		}
		dataPart, err := agentPart(event, &genai.Part{Text: `{"answer":"raw payload"}`}, exposeNone(), entrypoint, &output)
		if err != nil {
			t.Fatalf("part %d agentPart() error = %v", part, err)
		}
		data, ok := dataPart.Content.(a2a.Data)
		if !ok {
			t.Fatalf("part %d content = %#v, want structured data", part, dataPart.Content)
		}
		payload, payloadOK := data.Value.(map[string]any)
		if !payloadOK || fmt.Sprint(payload["answer"]) != " 最终结论 " {
			t.Fatalf("part %d data = %#v", part, data.Value)
		}
	}
	if parseCount != 1 {
		t.Fatalf("structured parses for 3 parts = %d, want 1", parseCount)
	}

	// A different final event must recompute instead of reusing the entry.
	event2 := &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"next"}`}}},
		},
		Actions: session.EventActions{StateDelta: map[string]any{
			execution.StructuredOutputStateKey(entrypoint.Name): map[string]any{"answer": " 第二次结论 "},
		}},
	}
	event2.Author = entrypoint.Name
	if _, applies, err := server.structuredOutput(event2, entrypoint); !applies || err != nil {
		t.Fatalf("second event structuredOutput() = applies=%v err=%v", applies, err)
	}
	if parseCount != 2 {
		t.Fatalf("structured parses after new event = %d, want 2", parseCount)
	}
}

func TestStructuredMemoMemoizesParseErrors(t *testing.T) {
	entrypoint := structuredEntrypoint(t)
	event := &session.Event{
		Actions: session.EventActions{StateDelta: map[string]any{
			execution.StructuredOutputStateKey(entrypoint.Name): "not-json",
		}},
	}
	event.Author = entrypoint.Name
	parseCount := 0
	server := &Server{structured: structuredMemo{
		parse: func(value any, definition *deploymentpkg.ResolvedAgentDefinition) (execution.OutputResult, error) {
			parseCount++
			return execution.ParseStructuredOutput(value, definition)
		},
	}}
	for attempt := 0; attempt < 2; attempt++ {
		if _, applies, err := server.structuredOutput(event, entrypoint); !applies || err == nil {
			t.Fatalf("attempt %d structuredOutput() = applies=%v err=%v, want parse error", attempt, applies, err)
		}
	}
	if parseCount != 1 {
		t.Fatalf("failed structured parses = %d, want 1", parseCount)
	}
	if _, applies, _ := server.structuredOutput(nil, entrypoint); applies {
		t.Fatal("nil event unexpectedly applied the structured branch")
	}
}

func TestAgentPartPreparsedResultMatchesInternalParse(t *testing.T) {
	entrypoint := structuredEntrypoint(t)
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"raw payload"}`}}},
		},
		Actions: session.EventActions{StateDelta: map[string]any{
			execution.StructuredOutputStateKey(entrypoint.Name): map[string]any{"answer": " 最终结论 "},
		}},
	}
	event.Author = entrypoint.Name

	internalPart, err := agentPart(event, &genai.Part{Text: `{"answer":"raw payload"}`}, exposeNone(), entrypoint, nil)
	if err != nil {
		t.Fatalf("internal-parse agentPart() error = %v", err)
	}
	parsed, err := execution.ParseStructuredOutput(
		event.Actions.StateDelta[execution.StructuredOutputStateKey(entrypoint.Name)], entrypoint,
	)
	if err != nil {
		t.Fatalf("ParseStructuredOutput() error = %v", err)
	}
	reusedPart, err := agentPart(event, &genai.Part{Text: `{"answer":"raw payload"}`}, exposeNone(), entrypoint, &parsed)
	if err != nil {
		t.Fatalf("preparsed agentPart() error = %v", err)
	}
	if fmt.Sprint(internalPart) != fmt.Sprint(reusedPart) {
		t.Fatalf("preparsed part %#v differs from internal-parse part %#v", reusedPart, internalPart)
	}
}

// TestAgentPartDecodesPreparsedCanonicalJSON pins the defensive fallback used
// when an OutputResult carries canonical JSON without its map-shaped decoded
// document. It must decode the retained bytes directly, without first copying
// them into a string.
func TestAgentPartDecodesPreparsedCanonicalJSON(t *testing.T) {
	entrypoint := structuredEntrypoint(t)
	event := &session.Event{
		Author: entrypoint.Name,
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"canonical"}`}}},
		},
		Actions: session.EventActions{StateDelta: map[string]any{
			execution.StructuredOutputStateKey(entrypoint.Name): map[string]any{"answer": "canonical"},
		}},
	}
	output := execution.OutputResult{
		JSON:     []byte(`{"answer":"canonical"}`),
		Text:     "canonical",
		Document: "defensive-fallback",
	}

	part, err := agentPart(event, &genai.Part{Text: `{"answer":"canonical"}`}, exposeNone(), entrypoint, &output)
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	data, ok := part.Content.(a2a.Data)
	if !ok {
		t.Fatalf("content = %#v, want structured data", part.Content)
	}
	payload, ok := data.Value.(map[string]any)
	if !ok || payload["answer"] != "canonical" {
		t.Fatalf("data = %#v, want canonical answer", data.Value)
	}
}

// exposeAll treats every author as plain-text visible; exposeNone hides every
// author's model text. Both stand in for a deployment-derived resolver.
func exposeAll() func(string) bool { return func(string) bool { return true } }

func exposeNone() func(string) bool { return func(string) bool { return false } }

// exposeByDeployment resolves model-text visibility per author from the
// deployment, mirroring the resolver built by a2a.New.
func exposeByDeployment(deployment *deploymentpkg.AgentDeployment) func(string) bool {
	return func(author string) bool {
		definition, ok := deployment.Agent(author)
		return ok && deploymentpkg.ExposesModelText(&definition.AgentDefinition)
	}
}

// visibilityDeployment models a plain-text entrypoint delegating to a
// structured expert, the topology that previously leaked raw model JSON.
func visibilityDeployment() *deploymentpkg.AgentDeployment {
	return &deploymentpkg.AgentDeployment{
		Metadata: deploymentpkg.DeploymentMetadata{Name: "t", Version: "1.0.0"},
		Spec: deploymentpkg.DeploymentSpec{Agents: []deploymentpkg.ResolvedAgentDefinition{
			{AgentDefinition: deploymentpkg.AgentDefinition{Name: "main"}},
			{AgentDefinition: deploymentpkg.AgentDefinition{Name: "worker", Output: deploymentpkg.OutputDefinition{TextPointer: "/answer"}}},
		}},
	}
}

func TestAgentPartPassesPlainTextThroughFrameworkMapping(t *testing.T) {
	event := &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "plain answer"}}},
		},
	}
	part, err := agentPart(event, &genai.Part{Text: "plain answer"}, exposeAll(), nil, nil)
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
	part, err := agentPart(event, &genai.Part{Text: `{"answer":"par`}, exposeNone(), structuredEntrypoint(t), nil)
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
			execution.StructuredOutputStateKey("main"): map[string]any{"answer": " 最终结论 "},
		}},
	}
	event.Author = "main"
	entrypoint := structuredEntrypoint(t)
	part, err := agentPart(event, &genai.Part{Text: `{"answer":"raw payload"}`}, exposeNone(), entrypoint, nil)
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
	part, err := agentPart(event, &genai.Part{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "test_tool"}}, exposeNone(), structuredEntrypoint(t), nil)
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
			execution.StructuredOutputStateKey("worker"): map[string]any{"answer": "worker answer"},
		}},
	}
	part, err := agentPart(event, &genai.Part{Text: `{"answer":"worker payload"}`}, exposeNone(), structuredEntrypoint(t), nil)
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
	part, err := agentPart(event, &genai.Part{Text: `{"answer":"internal","secret":true}`}, resolver, structuredEntrypoint(t), nil)
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
	part, err := agentPart(event, &genai.Part{Text: "orchestrator note"}, resolver, structuredEntrypoint(t), nil)
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
	part, err := agentPart(event, &genai.Part{Text: "client echo"}, exposeByDeployment(visibilityDeployment()), structuredEntrypoint(t), nil)
	if err != nil {
		t.Fatalf("agentPart() error = %v", err)
	}
	if part != nil {
		t.Fatalf("unknown author part = %#v, want suppressed", part)
	}
}

func structuredEntrypoint(t *testing.T) *deploymentpkg.ResolvedAgentDefinition {
	t.Helper()
	entrypoint, err := testagent.AgentDeployment(t).EntrypointDefinition()
	if err != nil {
		t.Fatal(err)
	}
	return entrypoint
}
