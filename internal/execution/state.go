package execution

import deployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"encoding/json"
	"strings"

	"google.golang.org/adk/v2/session"
)

// NewRunState returns the initial state for a run and selects plain-text capture.
func NewRunState(capturePlain bool) RunState {
	return RunState{CapturePlain: capturePlain}
}

// RunState is the immutable result state advanced across run events.
type RunState struct {
	Output       OutputResult
	CapturePlain bool
	tools        ToolNameTracker
}

// OutputResult is the validated final outcome of a run: the generic structured
// JSON when declared, plus the user-facing text.
type OutputResult struct {
	JSON json.RawMessage
	Text string
	// Document is the decoded, number-preserving form that JSON was
	// canonicalized from. It is derived data for adapters that need the parsed
	// value: JSON stays the canonical product and the wire contract.
	Document any
}

// ToolNameTracker preserves first-seen tool names without mutating its value.
type ToolNameTracker struct {
	seen []string
}

// Consume returns an updated tracker without mutating the current one. It
// allocates only when an event introduces a tool name not seen before, while
// copy-on-new preserves deterministic first-seen order.
func (t ToolNameTracker) Consume(event *session.Event) ToolNameTracker {
	if event == nil || event.Content == nil {
		return t
	}
	var nextSeen []string
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionResponse == nil {
			continue
		}
		name := part.FunctionResponse.Name
		seen := false
		for _, value := range t.seen {
			if value == name {
				seen = true
				break
			}
		}
		if !seen {
			for _, value := range nextSeen {
				if value == name {
					seen = true
					break
				}
			}
		}
		if !seen {
			if nextSeen == nil {
				nextSeen = make([]string, len(t.seen), len(t.seen)+len(event.Content.Parts))
				copy(nextSeen, t.seen)
			}
			nextSeen = append(nextSeen, name)
		}
	}
	if nextSeen == nil {
		return t
	}
	return ToolNameTracker{seen: nextSeen}
}

// Names returns a copy of first-seen tool names.
func (t ToolNameTracker) Names() []string {
	if len(t.seen) == 0 {
		return nil
	}
	return append([]string(nil), t.seen...)
}

// Consume is a pure event transition: it returns the next run state instead
// of mutating the current one. This keeps final-answer and tool-name rules
// testable without ADK, sessions, model clients, or audit dependencies.
func (s RunState) Consume(event *session.Event) RunState {
	next := s
	next.tools = s.tools.Consume(event)
	// A delegated plain-text agent authors the user-facing final answer even
	// though the entrypoint remains the deployment's root. Capture the last
	// terminal model response across the delegated tree.
	if !s.CapturePlain || event == nil || event.Partial || !event.IsFinalResponse() {
		return next
	}
	if event.Content == nil {
		return next
	}
	var text strings.Builder
	for _, part := range event.Content.Parts {
		if part == nil || part.Text == "" || part.Thought {
			continue
		}
		text.WriteString(part.Text)
	}
	if value := strings.TrimSpace(text.String()); value != "" {
		next.Output = OutputResult{Text: value}
	}
	return next
}

// Tools returns the tool names observed so far.
func (s RunState) Tools() []string {
	return s.tools.Names()
}

// ConsumeStructuredOutput validates and attaches final structured output.
func (s RunState) ConsumeStructuredOutput(
	value any,
	definition *deployment.ResolvedAgentDefinition,
) (RunState, error) {
	result, err := ParseStructuredOutput(value, definition)
	if err != nil {
		return s, err
	}
	next := s
	next.Output = result
	return next, nil
}

// ParseStructuredOutput is the single protocol-independent finalizer for ADK
// structured-output state. Every protocol receives the same validation,
// canonical JSON, and user-facing pointer text.
