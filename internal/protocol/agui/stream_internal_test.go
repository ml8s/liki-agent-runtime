package agui

import "testing"

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
