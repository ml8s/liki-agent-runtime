package execution

import (
	deployment "github.com/ml8s/liki-agent-runtime/internal/deployment"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// EventProjector is immutable configuration for converting internal ADK
// events into the runtime's observable event format. It is the single place
// that decides whether partial model output, finalized model output, and tool
// facts are visible to protocols.
type EventProjector struct {
	index EventProjectorIndex
}

// EventProjectionState is explicit per-run transition state. Project consumes
// the current state and returns the state used by the next event; the shared
// projector itself never changes.
type EventProjectionState struct {
	partialText map[string]bool
}

func (s EventProjectionState) clone() EventProjectionState {
	next := s
	if s.partialText == nil {
		return next
	}
	next.partialText = make(map[string]bool, len(s.partialText))
	for author, partial := range s.partialText {
		next.partialText[author] = partial
	}
	return next
}

func (s EventProjectionState) markPartial(author string) EventProjectionState {
	if s.partialText[author] {
		return s
	}
	next := s.clone()
	if next.partialText == nil {
		next.partialText = make(map[string]bool)
	}
	next.partialText[author] = true
	return next
}

func (s EventProjectionState) clearPartial(author string) EventProjectionState {
	if !s.partialText[author] {
		return s
	}
	next := s.clone()
	delete(next.partialText, author)
	return next
}

// EventProjectorIndex is the immutable deployment lookup shared by every run.
type EventProjectorIndex struct {
	definitions map[string]*deployment.ResolvedAgentDefinition
}

// NewEventProjectorIndex builds the deployment-scoped author lookup.
func NewEventProjectorIndex(aggregate *deployment.AgentDeployment) EventProjectorIndex {
	definitions := make(map[string]*deployment.ResolvedAgentDefinition, len(aggregate.Spec.Agents))
	for index := range aggregate.Spec.Agents {
		definition := &aggregate.Spec.Agents[index]
		definitions[definition.Name] = definition
	}
	return EventProjectorIndex{definitions: definitions}
}

// NewEventProjector builds a reusable projector from an immutable index.
func NewEventProjector(index EventProjectorIndex) EventProjector {
	return EventProjector{index: index}
}

// NewEventProjectionState returns the initial state for one run.
func NewEventProjectionState() EventProjectionState {
	return EventProjectionState{partialText: make(map[string]bool)}
}

// Project returns the observer-visible form of an ADK event.
//
// Plain-text Agents may stream user-facing text deltas. Structured Agents never
// expose raw model JSON; their selected user-facing text is projected after the
// runtime validates the complete output. Tool call/response facts are protocol
// events and remain visible, while thought and partial function-call chunks do
// not create duplicate protocol facts.
func (p EventProjector) Project(
	state EventProjectionState,
	event *session.Event,
) (*session.Event, EventProjectionState, bool) {
	if event == nil {
		return nil, state, false
	}
	definition, exposeModelText := p.definitionFor(event.Author)
	next := state

	var projected *session.Event
	if event.Partial {
		projected, next = next.projectPartial(event, definition, exposeModelText)
	} else {
		projected, next = next.projectFinal(event, definition, exposeModelText)
	}
	if projected == nil {
		return nil, next, false
	}
	return projected, next, true
}

func (s EventProjectionState) projectPartial(
	event *session.Event,
	definition *deployment.AgentDefinition, exposeModelText bool,
) (*session.Event, EventProjectionState) {
	if !exposeModelText {
		return nil, s
	}

	kept := 0
	for _, part := range event.Content.Parts {
		if part != nil && part.Text != "" && !part.Thought {
			kept++
		}
	}
	if kept == 0 {
		return nil, s
	}
	if definition != nil {
		s = s.markPartial(definition.Name)
	}
	if kept == len(event.Content.Parts) {
		return event, s
	}

	text := make([]*genai.Part, 0, kept)
	for _, part := range event.Content.Parts {
		if part != nil && part.Text != "" && !part.Thought {
			text = append(text, part)
		}
	}
	projected := *event
	projected.Content = &genai.Content{
		Role:  event.Content.Role,
		Parts: text,
	}
	return &projected, s
}

func (s EventProjectionState) projectFinal(
	event *session.Event,
	definition *deployment.AgentDefinition, exposeModelText bool,
) (*session.Event, EventProjectionState) {
	if event.Content == nil {
		// ADK can represent a terminal state transition without model content.
		// Keep the event visible so protocol adapters can consume StateDelta,
		// but never expose an absent model response as text.
		if event.Actions.StateDelta == nil {
			return nil, s
		}
		projected := *event
		projected.Content = nil
		return &projected, s
	}
	if definition != nil && !deployment.ExposesModelText(definition) {
		exposeModelText = false
	}
	if definition == nil {
		// Unknown or user-authored projections default to hiding model text;
		// explicit tool facts remain visible.
		exposeModelText = false
	}
	// The final aggregate repeats text already emitted as partial deltas.
	// Decide that once so the common fully-visible and fully-suppressed cases
	// can avoid allocating an intermediate part slice.
	suppressText := definition != nil && s.partialText[definition.Name]
	hasToolFact := false
	kept := 0

	for _, part := range event.Content.Parts {
		switch {
		case part == nil:
			continue
		case part.FunctionCall != nil || part.FunctionResponse != nil:
			hasToolFact = true
			kept++
		case exposeModelText && part.Text != "" && !part.Thought:
			if !suppressText {
				kept++
			}
		}
	}
	if kept == len(event.Content.Parts) {
		next := s
		if definition != nil {
			next = s.clearPartial(definition.Name)
		}
		return event, next
	}

	parts := make([]*genai.Part, 0, kept)
	for _, part := range event.Content.Parts {
		switch {
		case part == nil:
			continue
		case part.FunctionCall != nil || part.FunctionResponse != nil:
			parts = append(parts, part)
		case exposeModelText && part.Text != "" && !part.Thought:
			if !suppressText {
				parts = append(parts, part)
			}
		}
	}
	next := s
	if definition != nil {
		next = s.clearPartial(definition.Name)
	}

	// Preserve events that carry only runtime state (for example structured
	// output) even when their model text is not publicly streamable.
	if len(parts) == 0 && !hasToolFact && event.Actions.StateDelta != nil {
		projected := *event
		projected.Content = nil
		return &projected, next
	}
	if len(parts) == 0 {
		return nil, next
	}
	if len(parts) == len(event.Content.Parts) {
		return event, next
	}

	projected := *event
	projected.Content = &genai.Content{
		Role:  event.Content.Role,
		Parts: parts,
	}
	return &projected, next
}

func (p EventProjector) definitionFor(author string) (*deployment.AgentDefinition, bool) {
	definition, ok := p.index.definitions[author]
	if !ok || definition == nil {
		return nil, false
	}
	return &definition.AgentDefinition, deployment.ExposesModelText(&definition.AgentDefinition)
}
