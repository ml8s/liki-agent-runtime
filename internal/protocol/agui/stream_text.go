package agui

import (
	"fmt"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	aguitypes "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
)

func (s stream) openText(subagentID string) (stream, []aguievents.Event) {
	if s.state.textActive {
		return s, nil
	}
	next := s.clone()
	next.state.messageSequence++
	next.state.textMessageID = fmt.Sprintf("%s:assistant:%d", next.config.runID, next.state.messageSequence)
	next.state.textSubagentID = subagentID
	next.state.textActive = true
	if subagentID == "" {
		next.state.rootStreamedText = true
	}
	start := aguievents.NewTextMessageStartEvent(
		next.state.textMessageID,
		aguievents.WithRole(string(aguitypes.RoleAssistant)),
	)
	start.SubagentRunID = subagentID
	start.Name = next.activationName(subagentID)
	return next, []aguievents.Event{start}
}

func (s stream) emitTextDelta(delta, subagentID string) (stream, []aguievents.Event) {
	next, events := s.openText(subagentID)
	content := aguievents.NewTextMessageContentEvent(next.state.textMessageID, delta)
	content.SubagentRunID = subagentID
	return next, append(events, content)
}

func (s stream) closeText() (stream, []aguievents.Event) {
	if !s.state.textActive {
		return s, nil
	}
	next := s.clone()
	end := aguievents.NewTextMessageEndEvent(next.state.textMessageID)
	end.SubagentRunID = next.state.textSubagentID
	next.state.textActive = false
	next.state.textSubagentID = ""
	return next, []aguievents.Event{end}
}
