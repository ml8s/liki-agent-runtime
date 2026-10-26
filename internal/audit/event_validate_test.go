package audit

import (
	"testing"
	"time"
)

func TestValidateEventDoesNotMutateInput(t *testing.T) {
	input := Event{
		ID:         " run_1:run.started ",
		Type:       EventRunStarted,
		OccurredAt: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
		RootRunID:  " run_1 ",
		RunID:      " run_1 ",
		ThreadID:   " thread_1 ",
		UserID:     " user_1 ",
		AgentName:  " main ",
		Status:     StatusRunning,
	}

	normalized, err := ValidateEvent(input)
	if err != nil {
		t.Fatalf("ValidateEvent() error = %v", err)
	}
	if normalized.ID != "run_1:run.started" || normalized.AgentName != "main" {
		t.Fatalf("normalized event = %#v", normalized)
	}
	if input.ID != " run_1:run.started " || input.AgentName != " main " {
		t.Fatalf("input event = %#v, want unchanged", input)
	}
}
