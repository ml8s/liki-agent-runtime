package agui

import (
	"encoding/json"
	"strings"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"google.golang.org/adk/v2/session"
)

func (s stream) emitRunFinished(
	event *aguievents.RunFinishedEvent,
	emit func(aguievents.Event) error,
) (stream, error) {
	if s.state.runFinished {
		return s, nil
	}
	next := s
	next.state.runFinished = true
	return next, emit(event)
}

// streamConfig is immutable protocol identity for one AG-UI run.
type streamConfig struct {
	runID      string
	entrypoint string
}

// streamState is explicit transition state owned by one AG-UI run. consume
// receives the current stream and returns the stream for the next event.
type streamState struct {
	subagents        []*subagentActivation
	nextID           int
	toolSequence     int
	toolIDs          map[string]string
	messageSequence  int
	textMessageID    string
	textSubagentID   string
	textActive       bool
	rootStreamedText bool
	runFinished      bool
}

// stream pairs immutable protocol identity with explicit transition state.
type stream struct {
	config streamConfig
	state  streamState
}

func newStream(config streamConfig) stream {
	return stream{config: config}
}

func (s stream) consume(event *session.Event, emit func(aguievents.Event) error) (stream, error) {
	next := s
	if err := next.observeSubagent(event, emit); err != nil {
		return next, err
	}
	if event == nil || event.Content == nil {
		return next, nil
	}
	subagentID := next.subagentID(event)
	if next.state.textActive && next.state.textSubagentID != subagentID {
		if err := next.closeText(emit); err != nil {
			return next, err
		}
	}
	for _, part := range event.Content.Parts {
		if part == nil || part.Text == "" || part.Thought {
			continue
		}
		if err := next.emitTextDelta(part.Text, subagentID, emit); err != nil {
			return next, err
		}
	}

	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionCall == nil {
			continue
		}
		var callID string
		if err := next.closeText(emit); err != nil {
			return next, err
		}
		call := part.FunctionCall
		next.state, callID = nextToolCallID(next.state, next.config.runID, call.Name, call.ID)
		start := aguievents.NewToolCallStartEvent(callID, call.Name)
		start.SubagentRunID = subagentID
		if err := emit(start); err != nil {
			return next, err
		}
		args, err := json.Marshal(call.Args)
		if err != nil {
			args = []byte("{}")
		}
		delta := string(args)
		if delta == "" || delta == "null" {
			delta = "{}"
		}
		argsEvent := aguievents.NewToolCallArgsEvent(callID, delta)
		argsEvent.SubagentRunID = subagentID
		if err := emit(argsEvent); err != nil {
			return next, err
		}
		// AG-UI tool-call lifecycle: START -> ARGS -> END -> RESULT. END closes
		// the argument stream as soon as the call is specified, so a call whose
		// response never arrives is not left dangling.
		endEvent := aguievents.NewToolCallEndEvent(callID)
		endEvent.SubagentRunID = subagentID
		if err := emit(endEvent); err != nil {
			return next, err
		}
	}

	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionResponse == nil {
			continue
		}
		response := part.FunctionResponse
		if err := next.closeText(emit); err != nil {
			return next, err
		}
		responseID := next.toolCallID(response.Name, response.ID)
		payload, err := json.Marshal(response.Response)
		if err != nil || len(payload) == 0 || string(payload) == "null" {
			payload = []byte("{}")
		}
		resultEvent := aguievents.NewToolCallResultEvent(next.config.runID+":tools", responseID, string(payload))
		resultEvent.SubagentRunID = subagentID
		if err := emit(resultEvent); err != nil {
			return next, err
		}
		delete(next.state.toolIDs, response.Name)
	}

	return next, nil
}

// finish closes an active message and synthesizes the standard text lifecycle
// when the model returned only a structured final answer.
func (s stream) finish(content string, emit func(aguievents.Event) error) (stream, error) {
	next := s
	if err := next.finishSubagents(emit); err != nil {
		return next, err
	}
	if err := next.closeText(emit); err != nil {
		return next, err
	}
	if next.state.rootStreamedText {
		return next, nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return next, nil
	}
	if err := next.openText("", emit); err != nil {
		return next, err
	}
	if err := emit(aguievents.NewTextMessageContentEvent(next.state.textMessageID, content)); err != nil {
		return next, err
	}
	return next, next.closeText(emit)
}
