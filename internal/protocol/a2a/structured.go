package a2a

import (
	"sync"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/execution"
	"google.golang.org/adk/v2/session"
)

// structuredEvent is the memoized result for one final ADK event.
type structuredEvent struct {
	event  *session.Event
	output execution.OutputResult
	err    error
	valid  bool
}

// structuredMemo parses one final event's structured output once per attempt.
// The part converter is invoked once per content part, so the same event would
// otherwise repeat the decode/validate/canonicalize pipeline. Entries are
// attempt-scoped and released by A2A cleanup; this prevents the server from
// retaining the last raw ADK event after a task has finished.
type structuredMemo struct {
	mu       sync.Mutex
	attempts map[string]*structuredEvent

	// parse is a test seam; production always uses execution.ParseStructuredOutput.
	parse func(value any, definition *deploymentpkg.ResolvedAgentDefinition) (execution.OutputResult, error)
}

// structuredOutput returns the memoized parse when the event's state delta
// selects the structured branch. applies reports whether the branch matches.
func (s *Server) structuredOutput(
	attemptID string,
	event *session.Event,
	entrypoint *deploymentpkg.ResolvedAgentDefinition,
) (output execution.OutputResult, applies bool, err error) {
	if event == nil || entrypoint == nil ||
		event.Author != entrypoint.Name || !event.IsFinalResponse() {
		return execution.OutputResult{}, false, nil
	}
	value, ok := event.Actions.StateDelta[execution.StructuredOutputStateKey(entrypoint.Name)]
	if !ok {
		return execution.OutputResult{}, false, nil
	}

	s.structured.mu.Lock()
	if s.structured.attempts == nil {
		s.structured.attempts = make(map[string]*structuredEvent)
	}
	entry := s.structured.attempts[attemptID]
	if entry != nil && entry.valid && entry.event == event {
		output, err = entry.output, entry.err
		s.structured.mu.Unlock()
		return output, true, err
	}
	s.structured.mu.Unlock()

	output, err = s.structured.parse(value, entrypoint)

	s.structured.mu.Lock()
	s.structured.attempts[attemptID] = &structuredEvent{
		event:  event,
		output: output,
		err:    err,
		valid:  true,
	}
	s.structured.mu.Unlock()
	return output, true, err
}

// release drops the structured conversion cache after an attempt reaches its
// execution/cleanup boundary.
func (m *structuredMemo) release(attemptID string) {
	if m == nil || attemptID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.attempts, attemptID)
}
