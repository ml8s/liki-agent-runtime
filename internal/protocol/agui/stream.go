package agui

import (
	"encoding/json"
	"strings"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"google.golang.org/adk/v2/session"
)

// finishRun returns the terminal state and the RUN_FINISHED event. It is a
// no-op transition after the run has already finished.
func (s stream) finishRun(event *aguievents.RunFinishedEvent) (stream, []aguievents.Event) {
	if s.state.runFinished {
		return s, nil
	}
	next := s.clone()
	next.state.runFinished = true
	return next, []aguievents.Event{event}
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

func (s stream) clone() stream {
	next := s
	next.state = next.state.clone()
	return next
}

func (s stream) consume(event *session.Event) (stream, []aguievents.Event) {
	next, outgoing := s.observeSubagent(event)
	if event == nil || event.Content == nil {
		return next, outgoing
	}
	subagentID := next.subagentID(event)
	if next.state.textActive && next.state.textSubagentID != subagentID {
		var textEvents []aguievents.Event
		next, textEvents = next.closeText()
		outgoing = append(outgoing, textEvents...)
	}
	for _, part := range event.Content.Parts {
		if part == nil || part.Text == "" || part.Thought {
			continue
		}
		var textEvents []aguievents.Event
		next, textEvents = next.emitTextDelta(part.Text, subagentID)
		outgoing = append(outgoing, textEvents...)
	}
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionCall == nil {
			continue
		}
		textEvents := []aguievents.Event(nil)
		next, textEvents = next.closeText()
		outgoing = append(outgoing, textEvents...)
		call := part.FunctionCall
		var callID string
		next.state, callID = nextToolCallID(next.state, next.config.runID, call.Name, call.ID)
		start := aguievents.NewToolCallStartEvent(callID, call.Name)
		start.SubagentRunID = subagentID
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
		// AG-UI tool-call lifecycle: START -> ARGS -> END -> RESULT. END closes
		// the argument stream as soon as the call is specified, so a call whose
		// response never arrives is not left dangling.
		endEvent := aguievents.NewToolCallEndEvent(callID)
		endEvent.SubagentRunID = subagentID
		outgoing = append(outgoing, start, argsEvent, endEvent)
	}
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionResponse == nil {
			continue
		}
		textEvents := []aguievents.Event(nil)
		next, textEvents = next.closeText()
		outgoing = append(outgoing, textEvents...)
		response := part.FunctionResponse
		responseID := next.toolCallID(response.Name, response.ID)
		payload, err := json.Marshal(response.Response)
		if err != nil || len(payload) == 0 || string(payload) == "null" {
			payload = []byte("{}")
		}
		resultEvent := aguievents.NewToolCallResultEvent(next.config.runID+":tools", responseID, string(payload))
		resultEvent.SubagentRunID = subagentID
		outgoing = append(outgoing, resultEvent)
		next = next.releaseToolCallID(response.Name)
	}
	return next, outgoing
}

// finish closes an active message and synthesizes the standard text lifecycle
// when the model returned only a structured final answer.
func (s stream) finish(content string) (stream, []aguievents.Event) {
	next, outgoing := s.finishSubagents()
	textEvents := []aguievents.Event(nil)
	next, textEvents = next.closeText()
	outgoing = append(outgoing, textEvents...)
	if next.state.rootStreamedText {
		return next, outgoing
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return next, outgoing
	}
	var openEvents []aguievents.Event
	next, openEvents = next.openText("")
	outgoing = append(outgoing, openEvents...)
	if len(openEvents) == 0 {
		return next, outgoing
	}
	outgoing = append(outgoing, aguievents.NewTextMessageContentEvent(next.state.textMessageID, content))
	next, textEvents = next.closeText()
	return next, append(outgoing, textEvents...)
}

// clone returns a state whose mutable collections do not alias the receiver.
// subagentActivation values are immutable after creation, so their pointers can
// be copied directly.
func (s streamState) clone() streamState {
	next := s
	if s.subagents != nil {
		next.subagents = append([]*subagentActivation(nil), s.subagents...)
	}
	if s.toolIDs != nil {
		next.toolIDs = make(map[string]string, len(s.toolIDs))
		for toolName, id := range s.toolIDs {
			next.toolIDs[toolName] = id
		}
	}
	return next
}
