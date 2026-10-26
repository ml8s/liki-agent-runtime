package agent

import (
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestEventProjectorStreamsPlainTextDeltas(t *testing.T) {
	deployment := &Deployment{
		Metadata: DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{Agents: []AgentDefinition{{
			Name:        "main",
			Version:     "1.0.0",
			Description: "plain text agent",
			Mode:        AgentModeChat,
			Instruction: FileReference{Path: "instruction.md"},
			Tools:       ToolAllowlist{Allow: map[string][]string{}},
		}}},
	}
	projector := newEventProjector(newEventProjectorIndex(deployment))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "stream"}}},
		},
	}

	projected, visible := projector.Project(event)
	if !visible || projected == nil || len(projected.Content.Parts) != 1 || projected.Content.Parts[0].Text != "stream" {
		t.Fatalf("projected = %#v, visible = %v", projected, visible)
	}
}

func TestEventProjectorHidesStructuredPayload(t *testing.T) {
	deployment := &Deployment{
		Metadata: DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{Agents: []AgentDefinition{{
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
		}}},
	}
	projector := newEventProjector(newEventProjectorIndex(deployment))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: `{"answer":"raw"}`}}},
		},
	}

	projected, visible := projector.Project(event)
	if visible || projected != nil {
		t.Fatalf("projected = %#v, visible = %v; structured payload must be hidden", projected, visible)
	}
}

func TestEventProjectorPreservesStateOnlyFinalEvent(t *testing.T) {
	projector := newEventProjector(newEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{
		Author: "main",
		Actions: session.EventActions{
			StateDelta: map[string]any{StructuredOutputStateKey("main"): map[string]any{}},
		},
	}

	projected, visible := projector.Project(event)
	if !visible || projected == nil || projected.Content != nil {
		t.Fatalf("projected = %#v, visible = %v; want state-only event without content", projected, visible)
	}
}

func TestEventProjectorSuppressesFinalTextDuplicateAfterStreaming(t *testing.T) {
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

	if _, visible := projector.Project(partial); !visible {
		t.Fatal("partial text was not projected")
	}
	projected, visible := projector.Project(final)
	if visible || projected != nil {
		t.Fatalf("final duplicate = %#v, visible = %v; want suppressed", projected, visible)
	}
}

func TestEventProjectorKeepsToolFactWhenSuppressingFinalText(t *testing.T) {
	projector := newEventProjector(newEventProjectorIndex(plainTextDeployment()))
	partial := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "Hello "}}},
		},
	}
	if _, visible := projector.Project(partial); !visible {
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
	projected, visible := projector.Project(final)
	if !visible || projected == nil || len(projected.Content.Parts) != 1 {
		t.Fatalf("projected = %#v, visible = %v; want one tool fact", projected, visible)
	}
	if projected.Content.Parts[0].FunctionCall == nil {
		t.Fatalf("projected part = %#v, want function call", projected.Content.Parts[0])
	}
}

func plainTextDeployment() *Deployment {
	return &Deployment{
		Metadata: DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{Agents: []AgentDefinition{{
			Name:        "main",
			Version:     "1.0.0",
			Description: "plain text agent",
			Mode:        AgentModeChat,
			Instruction: FileReference{Path: "instruction.md"},
			Tools:       ToolAllowlist{Allow: map[string][]string{}},
		}}},
	}
}

func structuredTextDeployment() *Deployment {
	return &Deployment{
		Metadata: DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{Agents: []AgentDefinition{{
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
		}}},
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
		{name: "plain agent", definition: &plain, want: true},
		{name: "structured agent", definition: &structured, want: false},
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
	for _, deployment := range []*Deployment{plainTextDeployment(), structuredTextDeployment()} {
		entrypoint := deployment.Spec.Agents[0]
		projector := newEventProjector(newEventProjectorIndex(deployment))
		definition, expose := projector.definitionFor(entrypoint.Name)
		if definition == nil {
			t.Fatalf("definitionFor(%q) returned nil for structured=%v", entrypoint.Name, entrypoint.Output.Structured())
		}
		if expose != ExposesModelText(&entrypoint) {
			t.Fatalf("projector expose = %v, ExposesModelText = %v", expose, ExposesModelText(&entrypoint))
		}
	}
}
