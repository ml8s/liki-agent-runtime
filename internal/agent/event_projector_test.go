package agent

import (
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// BenchmarkEventProjectorFinalText measures the per-event cost paid by
// observing protocol streams. A plain final event is the common case where the
// projector can safely return the framework-native event unchanged.
func BenchmarkEventProjectorFinalText(b *testing.B) {
	deployment := plainTextDeployment()
	projection := newEventProjectionState()
	projector := newEventProjector(newEventProjectorIndex(deployment))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{{Text: "final answer"}}},
		},
	}

	b.ReportAllocs()
	for range b.N {
		_, _, visible := projector.Project(projection, event)
		if !visible {
			b.Fatal("event was not visible")
		}
	}
}

// BenchmarkEventProjectorPartialText measures the streaming path for a fully
// visible text delta, including per-run projector construction.
func BenchmarkEventProjectorPartialText(b *testing.B) {
	deployment := plainTextDeployment()
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "delta"}}},
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		projection := newEventProjectionState()
		projector := newEventProjector(newEventProjectorIndex(deployment))
		_, _, visible := projector.Project(projection, event)
		if !visible {
			b.Fatal("event was not visible")
		}
	}
}

func TestEventProjectorStreamsPlainTextDeltas(t *testing.T) {
	deployment := &AgentDeployment{
		Metadata: DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{Agents: resolvedAgentDefinitions([]AgentDefinition{{
			Name:        "main",
			Version:     "1.0.0",
			Description: "plain text agent",
			Mode:        AgentModeChat,
			Instruction: FileReference{Path: "instruction.md"},
			Tools:       ToolAllowlist{Allow: map[string][]string{}},
		}})},
	}
	projection := newEventProjectionState()
	projector := newEventProjector(newEventProjectorIndex(deployment))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "stream"}}},
		},
	}

	projected, _, visible := projector.Project(projection, event)
	if !visible || projected == nil || len(projected.Content.Parts) != 1 || projected.Content.Parts[0].Text != "stream" {
		t.Fatalf("projected = %#v, visible = %v", projected, visible)
	}
}

func TestEventProjectorHidesStructuredPayload(t *testing.T) {
	deployment := &AgentDeployment{
		Metadata: DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{Agents: resolvedAgentDefinitions([]AgentDefinition{{
			Name:        "main",
			Version:     "1.0.0",
			Description: "structured agent",
			Mode:        AgentModeChat,
			Instruction: FileReference{Path: "instruction.md"},
			Output: OutputDefinition{
				Schema:      FileReference{Path: "schema.json"},
				TextPointer: "/answer",
			},
			Tools: ToolAllowlist{Allow: map[string][]string{}},
		}})},
	}
	projection := newEventProjectionState()
	projector := newEventProjector(newEventProjectorIndex(deployment))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: `{"answer":"raw"}`}}},
		},
	}

	projected, _, visible := projector.Project(projection, event)
	if visible || projected != nil {
		t.Fatalf("projected = %#v, visible = %v; structured payload must be hidden", projected, visible)
	}
}

func TestEventProjectorPreservesStateOnlyFinalEvent(t *testing.T) {
	projection := newEventProjectionState()
	projector := newEventProjector(newEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{
		Author: "main",
		Actions: session.EventActions{
			StateDelta: map[string]any{StructuredOutputStateKey("main"): map[string]any{}},
		},
	}

	projected, _, visible := projector.Project(projection, event)
	if !visible || projected == nil || projected.Content != nil {
		t.Fatalf("projected = %#v, visible = %v; want state-only event without content", projected, visible)
	}
}

func TestEventProjectorSuppressesFinalTextDuplicateAfterStreaming(t *testing.T) {
	projection := newEventProjectionState()
	projector := newEventProjector(newEventProjectorIndex(plainTextDeployment()))
	partial := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello "}}},
		},
	}
	final := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello world"}}},
		},
	}

	_, projection, visible := projector.Project(projection, partial)
	if !visible {
		t.Fatal("partial text was not projected")
	}
	projected, _, visible := projector.Project(projection, final)
	if visible || projected != nil {
		t.Fatalf("final duplicate = %#v, visible = %v; want suppressed", projected, visible)
	}
}

func TestEventProjectorStateTransitionDoesNotMutatePreviousState(t *testing.T) {
	projection := newEventProjectionState()
	projector := newEventProjector(newEventProjectorIndex(plainTextDeployment()))
	partial := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello "}}},
		},
	}

	marked, markedNext, visible := projector.Project(projection, partial)
	if !visible || marked == nil {
		t.Fatalf("partial projected = %#v/%v, want visible text", marked, visible)
	}
	if !markedNext.partialText["main"] {
		t.Fatal("next projection state did not mark partial text")
	}
	if len(projection.partialText) != 0 {
		t.Fatalf("input projection state = %#v, want unchanged", projection.partialText)
	}

	final := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello world"}}},
		},
	}
	_, clearedNext, _ := projector.Project(markedNext, final)
	if len(clearedNext.partialText) != 0 {
		t.Fatalf("cleared projection state = %#v, want empty", clearedNext.partialText)
	}
	if len(markedNext.partialText) != 1 || !markedNext.partialText["main"] {
		t.Fatalf("marked projection state = %#v, want unchanged", markedNext.partialText)
	}
}

func TestEventProjectorKeepsToolFactWhenSuppressingFinalText(t *testing.T) {
	projection := newEventProjectionState()
	projector := newEventProjector(newEventProjectorIndex(plainTextDeployment()))
	partial := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello "}}},
		},
	}
	_, projection, visible := projector.Project(projection, partial)
	if !visible {
		t.Fatal("partial text was not projected")
	}

	final := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{
				{Text: "Hello world"},
				{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "test_tool"}},
			}},
		},
	}
	projected, _, visible := projector.Project(projection, final)
	if !visible || projected == nil || len(projected.Content.Parts) != 1 {
		t.Fatalf("projected = %#v, visible = %v; want one tool fact", projected, visible)
	}
	if projected.Content.Parts[0].FunctionCall == nil {
		t.Fatalf("projected part = %#v, want function call", projected.Content.Parts[0])
	}
}

func plainTextDeployment() *AgentDeployment {
	return &AgentDeployment{
		Metadata: DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{Agents: resolvedAgentDefinitions([]AgentDefinition{{
			Name:        "main",
			Version:     "1.0.0",
			Description: "plain text agent",
			Mode:        AgentModeChat,
			Instruction: FileReference{Path: "instruction.md"},
			Tools:       ToolAllowlist{Allow: map[string][]string{}},
		}})},
	}
}

func structuredTextDeployment() *AgentDeployment {
	return &AgentDeployment{
		Metadata: DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{Agents: resolvedAgentDefinitions([]AgentDefinition{{
			Name:        "main",
			Version:     "1.0.0",
			Description: "structured agent",
			Mode:        AgentModeChat,
			Instruction: FileReference{Path: "instruction.md"},
			Output: OutputDefinition{
				Schema:      FileReference{Path: "schema.json"},
				TextPointer: "/answer",
			},
			Tools: ToolAllowlist{Allow: map[string][]string{}},
		}})},
	}
}

func TestExposesModelText(t *testing.T) {
	plain := plainTextDeployment().Spec.Agents[0]
	structured := structuredTextDeployment().Spec.Agents[0]
	testCases := []struct {
		name       string
		definition *AgentDefinition
		want       bool
	}{
		{name: "nil definition", definition: nil, want: false},
		{name: "plain agent", definition: &plain.AgentDefinition, want: true},
		{name: "structured agent", definition: &structured.AgentDefinition, want: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ExposesModelText(testCase.definition); got != testCase.want {
				t.Fatalf("ExposesModelText() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestEventProjectorExposeDecisionMatchesRule locks the projector to the single
// visibility policy so a protocol adapter cannot silently diverge from it.
func TestEventProjectorExposeDecisionMatchesRule(t *testing.T) {
	for _, deployment := range []*AgentDeployment{plainTextDeployment(), structuredTextDeployment()} {
		entrypoint := deployment.Spec.Agents[0]
		projector := newEventProjector(newEventProjectorIndex(deployment))
		definition, expose := projector.definitionFor(entrypoint.Name)
		if definition == nil {
			t.Fatalf("definitionFor(%q) returned nil for structured=%v", entrypoint.Name, entrypoint.Output.Structured())
		}
		if expose != ExposesModelText(&entrypoint.AgentDefinition) {
			t.Fatalf("projector expose = %v, ExposesModelText = %v", expose, ExposesModelText(&entrypoint.AgentDefinition))
		}
	}
}
