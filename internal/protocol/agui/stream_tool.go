package agui

import (
	"fmt"
)

type openToolCall struct {
	ID       string
	ToolName string
}

func nextToolCallID(state streamState, runID, toolName, id string) (streamState, string) {
	if id != "" {
		return state, id
	}
	if state.toolIDs == nil {
		state.toolIDs = make(map[string]string)
	}
	if existing, ok := state.toolIDs[toolName]; ok {
		return state, existing
	}
	state = state.clone()
	if state.toolIDs == nil {
		state.toolIDs = make(map[string]string)
	}
	state.toolSequence++
	generated := fmt.Sprintf("%s:tools:%s:%d", runID, toolName, state.toolSequence)
	state.toolIDs[toolName] = generated
	return state, generated
}

func (s stream) toolCallID(toolName, id string) string {
	if id != "" {
		return id
	}
	return s.state.toolIDs[toolName]
}

func (s stream) releaseToolCallID(toolName string) stream {
	if _, ok := s.state.toolIDs[toolName]; !ok {
		return s
	}
	next := s.clone()
	delete(next.state.toolIDs, toolName)
	return next
}

func appendOpenToolCallID(state streamState, callID, toolName string) streamState {
	for _, existing := range state.openToolCalls {
		if existing.ID == callID {
			return state
		}
	}
	state = state.clone()
	state.openToolCalls = append(state.openToolCalls, openToolCall{ID: callID, ToolName: toolName})
	return state
}

func releaseOpenToolCallID(state streamState, callID string) streamState {
	for index, existing := range state.openToolCalls {
		if existing.ID != callID {
			continue
		}
		state = state.clone()
		state.openToolCalls = append(state.openToolCalls[:index], state.openToolCalls[index+1:]...)
		return state
	}
	return state
}
