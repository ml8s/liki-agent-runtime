package agui

import (
	"strconv"
	"strings"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"google.golang.org/adk/v2/session"
)

type subagentActivation struct {
	id     string
	name   string
	branch string
}

func (s *stream) subagentID(event *session.Event) string {
	if event == nil || event.Author == "" || event.Author == s.config.entrypoint {
		return ""
	}
	branch := effectiveBranch(event)
	for index := len(s.state.subagents) - 1; index >= 0; index-- {
		if isBranchPrefix(s.state.subagents[index].branch, branch) {
			return s.state.subagents[index].id
		}
	}
	return ""
}

func (s *stream) activationName(subagentID string) string {
	for _, activation := range s.state.subagents {
		if activation.id == subagentID {
			return activation.name
		}
	}
	return ""
}

func (s *stream) observeSubagent(event *session.Event, emit func(aguievents.Event) error) error {
	if event == nil || event.Author == "" || event.Author == s.config.entrypoint {
		return s.finishDeeperThan(eventBranch(event), emit)
	}
	branch := effectiveBranch(event)
	if err := s.finishDeeperThan(branch, emit); err != nil {
		return err
	}
	if branch == "" {
		return nil
	}
	for _, activation := range s.state.subagents {
		if activation.branch == branch {
			return nil
		}
	}

	s.state.nextID++
	activation := &subagentActivation{
		id:     s.config.runID + ":subagent:" + strconv.Itoa(s.state.nextID),
		name:   event.Author,
		branch: branch,
	}
	started := aguievents.NewSubagentStartedEvent(activation.id, activation.name)
	started.ParentSubagentRunID = s.parentID(branch)
	s.state.subagents = append(s.state.subagents, activation)
	return emit(started)
}

func eventBranch(event *session.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(event.Branch)
}

// effectiveBranch resolves an event's attribution branch. Task-mode
// sub-agents inherit the parent's branch — empty at the root — so events from
// a non-entrypoint author without a branch fall back to the author name as
// their attribution key. The reserved "user" author carries synthesized
// function responses and is never a sub-agent.
func effectiveBranch(event *session.Event) string {
	if branch := eventBranch(event); branch != "" {
		return branch
	}
	if event == nil || event.Author == "" || event.Author == "user" {
		return ""
	}
	return event.Author
}

func (s *stream) finishDeeperThan(branch string, emit func(aguievents.Event) error) error {
	closed := 0
	for index := len(s.state.subagents) - 1; index >= 0; index-- {
		activation := s.state.subagents[index]
		if isBranchPrefix(activation.branch, branch) {
			break
		}
		closed++
		if err := emit(aguievents.NewSubagentFinishedEvent(activation.id)); err != nil {
			return err
		}
	}
	if closed == 0 {
		return nil
	}
	s.state.subagents = s.state.subagents[:len(s.state.subagents)-closed]
	return nil
}

func (s *stream) finishSubagents(emit func(aguievents.Event) error) error {
	for index := len(s.state.subagents) - 1; index >= 0; index-- {
		if err := emit(aguievents.NewSubagentFinishedEvent(s.state.subagents[index].id)); err != nil {
			return err
		}
	}
	s.state.subagents = nil
	return nil
}

func (s stream) failSubagents(message, code string, emit func(aguievents.Event) error) (stream, error) {
	next := s
	for index := len(next.state.subagents) - 1; index >= 0; index-- {
		if err := emit(aguievents.NewSubagentErrorEvent(
			next.state.subagents[index].id,
			message,
			aguievents.WithSubagentErrorCode(code),
		)); err != nil {
			return next, err
		}
	}
	next.state.subagents = nil
	return next, nil
}

func (s *stream) parentID(branch string) string {
	parent := branch
	found := false
	if index := strings.LastIndex(branch, "."); index >= 0 {
		parent = branch[:index]
		found = true
	}
	if !found {
		return ""
	}
	for index := len(s.state.subagents) - 1; index >= 0; index-- {
		if s.state.subagents[index].branch == parent {
			return s.state.subagents[index].id
		}
	}
	return ""
}

func isBranchPrefix(prefix, branch string) bool {
	return prefix == branch || strings.HasPrefix(branch, prefix+".")
}
