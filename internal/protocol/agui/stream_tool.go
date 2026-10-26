package agui

import (
	"fmt"
)

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
	state.toolSequence++
	generated := fmt.Sprintf("%s:tools:%s:%d", runID, toolName, state.toolSequence)
	state.toolIDs[toolName] = generated
	return state, generated
}

func (s *stream) toolCallID(toolName, id string) string {
	if id != "" {
		return id
	}
	return s.state.toolIDs[toolName]
}
