package agent

import (
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// eventProjector is immutable configuration for converting internal ADK
// events into the runtime's observable event format. It is the single place
// that decides whether partial model output, finalized model output, and tool
// facts are visible to protocols.
type eventProjector struct {
	index eventProjectorIndex
}

// eventProjectionState is explicit per-run transition state. Project consumes
// the current state and returns the state used by the next event; the shared
// projector itself never changes.
type eventProjectionState struct {
	partialText map[string]bool
}

// eventProjectorIndex is the immutable deployment lookup shared by every run.
type eventProjectorIndex struct {
	definitions map[string]*AgentDefinition
}

func newEventProjectorIndex(deployment *AgentDeployment) eventProjectorIndex {
	definitions := make(map[string]*AgentDefinition, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		definition := &deployment.Spec.Agents[index]
		definitions[definition.Name] = definition
	}
	return eventProjectorIndex{definitions: definitions}
}

func newEventProjector(index eventProjectorIndex) eventProjector {
	return eventProjector{index: index}
}

func newEventProjectionState() eventProjectionState {
	return eventProjectionState{partialText: make(map[string]bool)}
}

// Project returns the observer-visible form of an ADK event.
//
// Plain-text Agents may stream user-facing text deltas. Structured Agents never
// expose raw model JSON; their selected user-facing text is projected after the
// runtime validates the complete output. Tool call/response facts are protocol
// events and remain visible, while thought and partial function-call chunks do
// not create duplicate protocol facts.
func (p eventProjector) Project(
	state eventProjectionState,
	event *session.Event,
) (*session.Event, eventProjectionState, bool) {
	if event == nil {
		return nil, state, false
	}
	definition, exposeModelText := p.definitionFor(event.Author)
	next := state

	var projected *session.Event
	if event.Partial {
		projected = next.projectPartial(event, definition, exposeModelText)
	} else {
		projected = next.projectFinal(event, definition, exposeModelText)
	}
	if projected == nil {
		return nil, next, false
	}
	return projected, next, true
}

func (s *eventProjectionState) projectPartial(
	event *session.Event,
	definition *AgentDefinition,
	exposeModelText bool,
) *session.Event {
	if !exposeModelText {
		return nil
	}

	kept := 0
	for _, part := range event.Content.Parts {
		if part != nil && part.Text != "" && !part.Thought {
			kept++
		}
	}
	if kept == 0 {
		return nil
	}
	if definition != nil {
		s.partialText[definition.Name] = true
	}
	if kept == len(event.Content.Parts) {
		return event
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
	return &projected
}

func (s *eventProjectionState) projectFinal(
	event *session.Event,
	definition *AgentDefinition,
	exposeModelText bool,
) *session.Event {
	if event.Content == nil {
		// ADK can represent a terminal state transition without model content.
		// Keep the event visible so protocol adapters can consume StateDelta,
		// but never expose an absent model response as text.
		if event.Actions.StateDelta == nil {
			return nil
		}
		projected := *event
		projected.Content = nil
		return &projected
	}
	if definition != nil && !ExposesModelText(definition) {
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
		if definition != nil {
			delete(s.partialText, definition.Name)
		}
		return event
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
	if definition != nil {
		delete(s.partialText, definition.Name)
	}

	// Preserve events that carry only runtime state (for example structured
	// output) even when their model text is not publicly streamable.
	if len(parts) == 0 && !hasToolFact && event.Actions.StateDelta != nil {
		projected := *event
		projected.Content = nil
		return &projected
	}
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == len(event.Content.Parts) {
		return event
	}

	projected := *event
	projected.Content = &genai.Content{
		Role:  event.Content.Role,
		Parts: parts,
	}
	return &projected
}

func (p eventProjector) definitionFor(author string) (*AgentDefinition, bool) {
	definition, ok := p.index.definitions[author]
	if !ok || definition == nil {
		return nil, false
	}
	return definition, ExposesModelText(definition)
}
