package audit

import "testing"

func TestNormalizeEventDoesNotMutateInput(t *testing.T) {
	event := Event{ID: " run_1:run.started ", AgentName: " main ", TraceID: "ABC"}
	normalized := normalizeEvent(event)
	if normalized.ID != "run_1:run.started" || normalized.AgentName != "main" || normalized.TraceID != "abc" {
		t.Fatalf("normalized = %#v", normalized)
	}
	if event.ID != " run_1:run.started " || event.AgentName != " main " || event.TraceID != "ABC" {
		t.Fatalf("input = %#v, want unchanged", event)
	}
}
