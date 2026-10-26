package agui

import (
	"fmt"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	aguitypes "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
)

func (s *stream) openText(subagentID string, emit func(aguievents.Event) error) error {
	if s.state.textActive {
		return nil
	}
	s.state.messageSequence++
	s.state.textMessageID = fmt.Sprintf("%s:assistant:%d", s.config.runID, s.state.messageSequence)
	s.state.textSubagentID = subagentID
	s.state.textActive = true
	if subagentID == "" {
		s.state.rootStreamedText = true
	}
	start := aguievents.NewTextMessageStartEvent(
		s.state.textMessageID,
		aguievents.WithRole(string(aguitypes.RoleAssistant)),
	)
	start.SubagentRunID = subagentID
	start.Name = s.activationName(subagentID)
	return emit(start)
}

func (s *stream) emitTextDelta(delta, subagentID string, emit func(aguievents.Event) error) error {
	if err := s.openText(subagentID, emit); err != nil {
		return err
	}
	content := aguievents.NewTextMessageContentEvent(s.state.textMessageID, delta)
	content.SubagentRunID = subagentID
	return emit(content)
}

func (s *stream) closeText(emit func(aguievents.Event) error) error {
	if !s.state.textActive {
		return nil
	}
	end := aguievents.NewTextMessageEndEvent(s.state.textMessageID)
	end.SubagentRunID = s.state.textSubagentID
	if err := emit(end); err != nil {
		return err
	}
	s.state.textActive = false
	s.state.textSubagentID = ""
	return nil
}
