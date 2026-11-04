package agent

import (
	"strings"

	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/execution"

	"google.golang.org/adk/v2/session"
)

// RunState is the immutable result state advanced across ADK run events.
type RunState struct {
	Output execution.OutputResult
	// textPolicy is the aggregate-level single source of truth for which
	// author's model text may become user-visible state.
	textPolicy  TextVisibilityPolicy
	toolTracker toolNameTracker
}

// TextVisibilityPolicy resolves author visibility independently of protocol.
type TextVisibilityPolicy interface {
	ExposesModelText(author string) bool
}

// NewRunState returns the initial state governed by a visibility policy.
// A nil policy captures no model text, preserving fail-closed behavior.
func NewRunState(textPolicy TextVisibilityPolicy) RunState {
	return RunState{textPolicy: textPolicy}
}

// toolNameTracker preserves first-seen tool names without mutating its value.
type toolNameTracker struct {
	seen  map[string]struct{}
	order []string
}

// consume returns an updated tracker without mutating the current one. It
// allocates only when an event introduces a tool name not seen before, while
// copy-on-new preserves deterministic first-seen order.
func (t toolNameTracker) consume(event *session.Event) toolNameTracker {
	if event == nil || event.Content == nil {
		return t
	}
	var nextTracker *toolNameTracker
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionResponse == nil {
			continue
		}
		name := part.FunctionResponse.Name
		if _, seen := t.seen[name]; seen {
			continue
		}
		if nextTracker == nil {
			nextTracker = &toolNameTracker{
				seen:  make(map[string]struct{}, len(t.seen)+1),
				order: append([]string(nil), t.order...),
			}
			for _, seen := range t.order {
				nextTracker.seen[seen] = struct{}{}
			}
		}
		if _, seen := nextTracker.seen[name]; seen {
			continue
		}
		nextTracker.seen[name] = struct{}{}
		nextTracker.order = append(nextTracker.order, name)
	}
	if nextTracker == nil {
		return t
	}
	return *nextTracker
}

// names returns a copy of first-seen tool names.
func (t toolNameTracker) names() []string {
	if len(t.seen) == 0 {
		return nil
	}
	return append([]string(nil), t.order...)
}

// Consume is a pure ADK event transition: it returns the next run state
// instead of mutating the current one.
func (s RunState) Consume(event *session.Event) RunState {
	next := s
	next.toolTracker = s.toolTracker.consume(event)
	if event == nil || event.Partial || !event.IsFinalResponse() ||
		!s.textPolicy.ExposesModelText(event.Author) {
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
		next.Output = execution.OutputResult{Text: value}
	}
	return next
}

// Tools returns the tool names observed so far.
func (s RunState) Tools() []string {
	return s.toolTracker.names()
}

// ConsumeStructuredOutput validates and attaches final structured output.
func (s RunState) ConsumeStructuredOutput(
	value any,
	definition *agentdeployment.ResolvedAgentDefinition,
) (RunState, error) {
	result, err := execution.ParseStructuredOutput(value, definition)
	if err != nil {
		return s, err
	}
	next := s
	next.Output = result
	return next, nil
}
