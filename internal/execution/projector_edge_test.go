package execution

import (
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestEventProjectorPartialAllThoughtParts(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{
				{Text: "thinking", Thought: true},
			}},
		},
	}
	projected, _, visible := projector.Project(projection, event)
	if visible || projected != nil {
		t.Fatalf("thought-only partial projected = %#v/%v, want hidden", projected, visible)
	}
}

func TestEventProjectorPartialMixedThoughtAndText(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{
				{Text: "thinking", Thought: true},
				{Text: "visible"},
			}},
		},
	}
	projected, next, visible := projector.Project(projection, event)
	if !visible || projected == nil || len(projected.Content.Parts) != 1 {
		t.Fatalf("mixed partial = %#v/%v, want one visible text part", projected, visible)
	}
	if projected.Content.Parts[0].Text != "visible" {
		t.Fatalf("part text = %q, want visible", projected.Content.Parts[0].Text)
	}
	if !next.partialText["main"] {
		t.Fatal("mixed partial did not mark the author as streaming")
	}
}

func TestEventProjectorPartialNilPart(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{nil}},
		},
	}
	_, _, visible := projector.Project(projection, event)
	if visible {
		t.Fatal("nil-only partial was visible, want hidden")
	}
}

func TestEventProjectorPartialNilEvent(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))
	_, _, visible := projector.Project(projection, nil)
	if visible {
		t.Fatal("nil event was visible, want hidden")
	}
}

func TestEventProjectorPartialMixedNilAndText(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{
				nil,
				{Text: "kept"},
			}},
		},
	}
	projected, _, visible := projector.Project(projection, event)
	if !visible || projected == nil {
		t.Fatalf("mixed nil+text partial = %v, want visible", visible)
	}
	if len(projected.Content.Parts) != 1 || projected.Content.Parts[0].Text != "kept" {
		t.Fatalf("parts = %#v, want one kept text part", projected.Content.Parts)
	}
}

func TestEventProjectorFinalNilContentNilStateDelta(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{Author: "main"}
	_, _, visible := projector.Project(projection, event)
	if visible {
		t.Fatal("nil content and nil state delta was visible, want hidden")
	}
}

func TestEventProjectorFinalStructuredKeepsStateDelta(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(structuredTextDeployment()))
	event := &session.Event{
		Author: "main",
		Actions: session.EventActions{
			StateDelta: map[string]any{StructuredOutputStateKey("main"): "value"},
		},
	}
	projected, _, visible := projector.Project(projection, event)
	if !visible || projected == nil {
		t.Fatal("structured state-only final event was hidden, want visible")
	}
	if projected.Content != nil {
		t.Fatal("structured state-only event should have nil content")
	}
}

func TestEventProjectorFinalUnknownAuthorHidesText(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{
		Author: "unknown",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{{Text: "secret"}}},
		},
	}
	_, _, visible := projector.Project(projection, event)
	if visible {
		t.Fatal("unknown author text was visible, want hidden")
	}
}

func TestEventProjectorFinalToolOnlyEventKept(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "1", Name: "tool"}},
			}},
		},
	}
	projected, next, visible := projector.Project(projection, event)
	if !visible || projected == nil || len(projected.Content.Parts) != 1 {
		t.Fatalf("tool-only final = %#v/%v, want one tool fact", projected, visible)
	}
	if next.partialText["main"] {
		t.Fatal("tool-only final should clear partial marking")
	}
}

func TestEventProjectorPartialMarkClearOnFullMatchFinal(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))

	partial := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "Hi"}}},
		},
	}
	_, state, visible := projector.Project(projection, partial)
	if !visible {
		t.Fatal("partial text was hidden, want visible")
	}

	// Final with the exact same parts should take the fast path and clear.
	final := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{{Text: "Hi"}}},
		},
	}
	_, next, _ := projector.Project(state, final)
	if next.partialText["main"] {
		t.Fatal("full-match final should clear partialText")
	}
}

func TestEventProjectorPartialTextAfterToolFactFinal(t *testing.T) {
	projection := NewEventProjectionState()
	projector := NewEventProjector(NewEventProjectorIndex(plainTextDeployment()))

	partial := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Partial: true,
			Content: &genai.Content{Parts: []*genai.Part{{Text: "stream"}}},
		},
	}
	_, state, _ := projector.Project(projection, partial)

	// Final with both tool fact and repeated text should keep the tool fact
	// but suppress the already-streamed text.
	final := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{
				{Text: "stream"},
				{FunctionResponse: &genai.FunctionResponse{ID: "1", Name: "tool", Response: map[string]any{}}},
			}},
		},
	}
	projected, _, visible := projector.Project(state, final)
	if !visible || projected == nil {
		t.Fatalf("tool+text final = %v, want visible", visible)
	}
	if len(projected.Content.Parts) != 1 {
		t.Fatalf("parts = %d, want 1 tool fact only", len(projected.Content.Parts))
	}
	if projected.Content.Parts[0].FunctionResponse == nil {
		t.Fatal("kept part is not a function response")
	}
}
