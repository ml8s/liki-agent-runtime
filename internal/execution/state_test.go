package execution

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	deployment "github.com/ml8s/liki-agent-runtime/internal/deployment"
)

func loadedTestDeployment(t testing.TB) *deployment.AgentDeployment {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"agent-deployment.json": `{
			"schema_version": "liki.agent-deployment.v1",
			"metadata": {"name": "test-agent", "version": "1.0.0"},
			"spec": {
				"mcpServers": [],
				"agents": [{
					"name": "main",
					"version": "1.0.0",
					"description": "structured test agent",
					"mode": "chat",
					"instruction": {"path": "instruction.md"},
					"output": {
						"schema": {"path": "output.schema.json"},
						"textPointer": "/answer"
					},
					"tools": {"allow": {}}
				}]
			}
		}`,
		"instruction.md": "You are a generic test agent.",
		"output.schema.json": `{
			"type": "object",
			"additionalProperties": false,
			"required": ["answer"],
			"properties": {"answer": {"type": "string"}, "confidence": {"type": "number", "minimum": 0, "maximum": 1}}
		}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := deployment.LoadAgentDeployment(filepath.Join(root, "agent-deployment.json"))
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func textEvent(author, text string, partial, final bool) *session.Event {
	event := &session.Event{
		Author: author,
		LLMResponse: model.LLMResponse{
			Partial: partial,
			Content: &genai.Content{Parts: []*genai.Part{{Text: text}}},
		},
	}
	if final {
		event.TurnComplete = true
	}
	return event
}

func responsePart(name, callID string) *genai.Part {
	return &genai.Part{
		FunctionResponse: &genai.FunctionResponse{
			ID: callID, Name: name, Response: map[string]any{},
		},
	}
}

func functionResponseEvent(author, name, callID string) *session.Event {
	part := responsePart(name, callID)
	return &session.Event{
		Author: author,
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{part}},
		},
	}
}

func TestRunStateConsumeCapturesPlainFinalText(t *testing.T) {
	state := RunState{CapturePlain: true}

	if next := state.Consume(nil); next.Output.Text != "" {
		t.Fatalf("nil event output = %q, want empty", next.Output.Text)
	}

	partial := state.Consume(textEvent("main", "partial", true, false))
	if partial.Output.Text != "" {
		t.Fatalf("partial output = %q, want empty", partial.Output.Text)
	}

	final := partial.Consume(textEvent("main", "answer", false, true))
	if final.Output.Text != "answer" {
		t.Fatalf("final output = %q, want answer", final.Output.Text)
	}

	last := final.Consume(textEvent("main", "latest", false, true))
	if last.Output.Text != "latest" {
		t.Fatalf("second final output = %q, want latest", last.Output.Text)
	}
}

func TestRunStateConsumeSkipsThoughtAndEmptyParts(t *testing.T) {
	state := RunState{CapturePlain: true}
	event := &session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{
				{Text: "thought", Thought: true},
				{Text: "   "},
			}},
		},
	}
	next := state.Consume(event)
	if next.Output.Text != "" {
		t.Fatalf("thought/whitespace output = %q, want empty", next.Output.Text)
	}
}

func TestRunStateConsumeNoCapturePlain(t *testing.T) {
	state := RunState{CapturePlain: false}
	next := state.Consume(textEvent("main", "answer", false, true))
	if next.Output.Text != "" {
		t.Fatalf("CapturePlain=false output = %q, want empty", next.Output.Text)
	}
}

func TestRunStateConsumeNonFinalNoContent(t *testing.T) {
	state := RunState{CapturePlain: true}
	next := state.Consume(&session.Event{
		Author:      "main",
		LLMResponse: model.LLMResponse{Partial: true},
	})
	if next.Output.Text != "" {
		t.Fatalf("nil content output = %q, want empty", next.Output.Text)
	}
}

func TestToolNameTrackerDedup(t *testing.T) {
	var tracker ToolNameTracker
	if names := tracker.Names(); names != nil {
		t.Fatalf("empty Names() = %v, want nil", names)
	}

	tracker = tracker.Consume(functionResponseEvent("main", "tool_a", "1"))
	tracker = tracker.Consume(functionResponseEvent("main", "tool_b", "2"))
	tracker = tracker.Consume(functionResponseEvent("main", "tool_a", "3"))

	names := tracker.Names()
	if len(names) != 2 || names[0] != "tool_a" || names[1] != "tool_b" {
		t.Fatalf("Names() = %v, want [tool_a tool_b]", names)
	}

	names[0] = "mutated"
	if tracker.Names()[0] != "tool_a" {
		t.Fatal("Names() returned a non-defensive copy")
	}
}

func TestToolNameTrackerSkipsNonResponseParts(t *testing.T) {
	var tracker ToolNameTracker
	tracker = tracker.Consume(nil)
	tracker = tracker.Consume(&session.Event{
		Author: "main",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{
				{Text: "text"},
				{FunctionCall: &genai.FunctionCall{ID: "1", Name: "tool"}},
			}},
		},
	})
	if names := tracker.Names(); names != nil {
		t.Fatalf("Names() = %v, want nil", names)
	}
}

func TestRunStateTools(t *testing.T) {
	state := RunState{CapturePlain: true}
	if tools := state.Tools(); tools != nil {
		t.Fatalf("initial Tools() = %v, want nil", tools)
	}

	state = state.Consume(functionResponseEvent("main", "tool_a", "1"))
	state = state.Consume(functionResponseEvent("main", "tool_b", "2"))

	tools := state.Tools()
	if len(tools) != 2 || tools[0] != "tool_a" || tools[1] != "tool_b" {
		t.Fatalf("Tools() = %v, want [tool_a tool_b]", tools)
	}
}

// TestRunStateConsumeIsImmutableForToolNames verifies the value semantics of
// RunState across tool-tracking transitions.
func TestRunStateConsumeIsImmutableForToolNames(t *testing.T) {
	state := RunState{}
	response := func(name string) *session.Event {
		return functionResponseEvent("worker", name, "1")
	}

	state = state.Consume(response("tool_a"))
	first := state
	state = state.Consume(response("tool_a"))
	state = state.Consume(response("tool_b"))

	want := []string{"tool_a", "tool_b"}
	if got := state.Tools(); !slices.Equal(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
	if got := first.Tools(); !slices.Equal(got, want[:1]) {
		t.Fatalf("previous state tool names = %v, want %v", got, want[:1])
	}
}

// TestRunStateConsumeTracksMultipleToolsInOneEvent covers a single event
// carrying several FunctionResponse parts (parallel tool calls).
func TestRunStateConsumeTracksMultipleToolsInOneEvent(t *testing.T) {
	state := RunState{}
	event := &session.Event{LLMResponse: model.LLMResponse{
		Content: &genai.Content{Parts: []*genai.Part{
			responsePart("tool_a", "1"),
			responsePart("tool_b", "2"),
			responsePart("tool_a", "3"),
		}},
	}}

	next := state.Consume(event)
	want := []string{"tool_a", "tool_b"}
	if got := next.Tools(); !slices.Equal(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
	if len(state.Tools()) != 0 {
		t.Fatalf("input state tool names = %v, want empty", state.Tools())
	}
}

// TestRunStateConsumeCapturesDelegatedAuthorText verifies that the plain-text
// capture does not require the entrypoint author; any agent's terminal final
// response is user-facing.
func TestRunStateConsumeCapturesDelegatedAuthorText(t *testing.T) {
	state := RunState{CapturePlain: true}
	event := textEvent("bazi", "delegated answer", false, true)
	next := state.Consume(event)
	if next.Output.Text != "delegated answer" {
		t.Fatalf("output text = %q", next.Output.Text)
	}
	if state.Output.Text != "" {
		t.Fatalf("input state text = %q, want unchanged", state.Output.Text)
	}
}
