package agent

import (
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// eventProjector converts internal ADK events into the runtime's observable
// event format. It is the single place that decides whether partial model
// output, finalized model output, and tool facts are visible to protocols.
type eventProjector struct {
	definitions map[string]*AgentDefinition
	partialText map[string]bool
}

func newEventProjector(deployment *Deployment) eventProjector {
	definitions := make(map[string]*AgentDefinition, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		definition := &deployment.Spec.Agents[index]
		definitions[definition.Name] = definition
	}
	return eventProjector{
		definitions: definitions,
		partialText: make(map[string]bool),
	}
}

// Project returns the observer-visible form of an ADK event.
//
// Plain-text Agents may stream user-facing text deltas. Structured Agents never
// expose raw model JSON; their selected user-facing text is projected after the
// runtime validates the complete output. Tool call/response facts are protocol
// events and remain visible, while thought and partial function-call chunks do
// not create duplicate protocol facts.
func (p eventProjector) Project(event *session.Event) (*session.Event, bool) {
	if event == nil {
		return nil, false
	}
	definition, exposeModelText := p.definitionFor(event.Author)

	var projected *session.Event
	if event.Partial {
		projected = p.projectPartial(event, definition, exposeModelText)
	} else {
		projected = p.projectFinal(event, definition, exposeModelText)
	}
	if projected == nil {
		return nil, false
	}
	return projected, true
}

func (p eventProjector) projectPartial(
	event *session.Event,
	definition *AgentDefinition,
	exposeModelText bool,
) *session.Event {
	if !exposeModelText {
		return nil
	}

	text := nonThoughtTextParts(event)
	if len(text) == 0 {
		return nil
	}

	if definition != nil {
		p.partialText[definition.Name] = true
	}
	projected := *event
	projected.Content = &genai.Content{
		Role:  event.Content.Role,
		Parts: text,
	}
	return &projected
}

func (p eventProjector) projectFinal(
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
	parts := make([]*genai.Part, 0, len(event.Content.Parts))
	hasToolFact := false

	for _, part := range event.Content.Parts {
		switch {
		case part == nil:
			continue
		case part.FunctionCall != nil || part.FunctionResponse != nil:
			parts = append(parts, part)
			hasToolFact = true
		case exposeModelText && part.Text != "" && !part.Thought:
			parts = append(parts, part)
		}
	}

	if definition != nil && p.partialText[definition.Name] {
		// The text was already projected as partial deltas. A streaming model's
		// final aggregate repeats the same text and must not be emitted twice.
		parts = filterParts(parts, func(part *genai.Part) bool {
			return part.Text == "" || part.Thought
		})
	}
	if definition != nil {
		delete(p.partialText, definition.Name)
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
	definition, ok := p.definitions[author]
	if !ok || definition == nil {
		return nil, false
	}
	return definition, ExposesModelText(definition)
}

func nonThoughtTextParts(event *session.Event) []*genai.Part {
	if event == nil || event.Content == nil {
		return nil
	}
	parts := make([]*genai.Part, 0, len(event.Content.Parts))
	for _, part := range event.Content.Parts {
		if part == nil || part.Text == "" || part.Thought {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

func filterParts(parts []*genai.Part, keep func(*genai.Part) bool) []*genai.Part {
	filtered := make([]*genai.Part, 0, len(parts))
	for _, part := range parts {
		if part != nil && keep(part) {
			filtered = append(filtered, part)
		}
	}
	return filtered
}
