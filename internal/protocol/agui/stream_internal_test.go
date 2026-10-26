package agui

import (
	"testing"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestNextToolCallIDGeneratesUniqueFallbacks(t *testing.T) {
	state := streamState{}
	state, first := nextToolCallID(state, "run_1", "same_tool", "")
	// A completed tool call releases its generated fallback ID, allowing a
	// later invocation of the same tool to receive a fresh ID.
	delete(state.toolIDs, "same_tool")
	state, second := nextToolCallID(state, "run_1", "same_tool", "")
	state, explicit := nextToolCallID(state, "run_1", "other_tool", "caller_id")

	if first == "" || second == "" || first == second {
		t.Fatalf("fallback tool IDs = %q/%q, want unique values", first, second)
	}
	if explicit != "caller_id" {
		t.Fatalf("explicit tool ID = %q, want caller_id", explicit)
	}
	if state.toolSequence != 2 || state.toolIDs["same_tool"] != second {
		t.Fatalf("next state = %#v, want explicit sequence and lookup state", state)
	}
}

func TestStreamConsumeDoesNotMutatePreviousState(t *testing.T) {
	stream := newStream(streamConfig{runID: "run_1", entrypoint: "main"})
	event := &session.Event{
		Author: "worker",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Parts: []*genai.Part{{Text: "delta"}}},
		},
	}

	next, events := stream.consume(event)
	if len(stream.state.subagents) != 0 || stream.state.textActive {
		t.Fatalf("previous stream state = %#v, want unchanged", stream.state)
	}
	if len(next.state.subagents) != 1 || !next.state.textActive {
		t.Fatalf("next stream state = %#v, want subagent and active text", next.state)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want start/content/end", len(events))
	}
}

func TestStreamToolCallStateDoesNotAliasPreviousState(t *testing.T) {
	stream := newStream(streamConfig{runID: "run_1", entrypoint: "main"})
	event := &session.Event{LLMResponse: model.LLMResponse{
		Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "tool"}},
			{FunctionResponse: &genai.FunctionResponse{Name: "tool", Response: map[string]any{"ok": true}}},
		}},
	}}

	next, events := stream.consume(event)
	if stream.state.toolIDs != nil || stream.state.toolSequence != 0 {
		t.Fatalf("previous stream state = %#v, want unchanged", stream.state)
	}
	if next.state.toolSequence != 1 {
		t.Fatalf("tool sequence = %d, want 1", next.state.toolSequence)
	}
	if _, ok := next.state.toolIDs["tool"]; ok {
		t.Fatalf("tool ID = %#v, want released after response", next.state.toolIDs)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want start/args/end/result", len(events))
	}
}

func TestStreamFinishDoesNotMutatePreviousState(t *testing.T) {
	stream := newStream(streamConfig{runID: "run_1", entrypoint: "main"})
	stream.state.subagents = []*subagentActivation{{id: "worker", name: "worker", branch: "worker"}}
	stream.state.textActive = true
	stream.state.textMessageID = "active"
	stream.state.textSubagentID = "worker"

	next, events := stream.finish("answer")
	if len(stream.state.subagents) != 1 || !stream.state.textActive {
		t.Fatalf("previous stream state = %#v, want unchanged", stream.state)
	}
	if len(next.state.subagents) != 0 || next.state.textActive {
		t.Fatalf("next stream state = %#v, want closed subagent and text", next.state)
	}
	if len(events) != 5 {
		t.Fatalf("events = %d, want finish/close/start/content/end", len(events))
	}
	if events[0].Type() != "SUBAGENT_FINISHED" || events[1].Type() != "TEXT_MESSAGE_END" {
		t.Fatalf("closing events = %#v", events[:2])
	}
}

func TestFinishRunDoesNotMutatePreviousState(t *testing.T) {
	stream := newStream(streamConfig{runID: "run_1", entrypoint: "main"})
	finished := aguievents.NewRunFinishedEventWithOptions(
		"thread_1",
		"run_1",
		aguievents.WithSuccessOutcome(),
	)

	next, events := stream.finishRun(finished)
	if stream.state.runFinished {
		t.Fatal("previous stream state was mutated")
	}
	if !next.state.runFinished || len(events) != 1 {
		t.Fatalf("next state = %#v, events = %d", next.state, len(events))
	}
	repeatedNext, repeatedEvents := next.finishRun(finished)
	if !repeatedNext.state.runFinished || len(repeatedEvents) != 0 {
		t.Fatalf("repeat transition = %#v, events = %d, want no-op", repeatedNext.state, len(repeatedEvents))
	}
}
