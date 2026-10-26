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

func (s stream) subagentID(event *session.Event) string {
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

func (s stream) activationName(subagentID string) string {
	for _, activation := range s.state.subagents {
		if activation.id == subagentID {
			return activation.name
		}
	}
	return ""
}

func (s stream) observeSubagent(event *session.Event) (stream, []aguievents.Event) {
	if event == nil || event.Author == "" || event.Author == s.config.entrypoint {
		return s.finishDeeperThan(eventBranch(event))
	}
	branch := effectiveBranch(event)
	next, events := s.finishDeeperThan(branch)
	if branch == "" {
		return next, events
	}
	for _, activation := range next.state.subagents {
		if activation.branch == branch {
			return next, events
		}
	}

	next = next.clone()
	next.state.nextID++
	activation := &subagentActivation{
		id:     next.config.runID + ":subagent:" + strconv.Itoa(next.state.nextID),
		name:   event.Author,
		branch: branch,
	}
	started := aguievents.NewSubagentStartedEvent(activation.id, activation.name)
	started.ParentSubagentRunID = next.parentID(branch)
	next.state.subagents = append(next.state.subagents, activation)
	return next, append(events, started)
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

func (s stream) finishDeeperThan(branch string) (stream, []aguievents.Event) {
	next := s.clone()
	closed := 0
	var events []aguievents.Event
	for index := len(next.state.subagents) - 1; index >= 0; index-- {
		activation := next.state.subagents[index]
		if isBranchPrefix(activation.branch, branch) {
			break
		}
		closed++
		events = append(events, aguievents.NewSubagentFinishedEvent(activation.id))
	}
	if closed == 0 {
		return s, nil
	}
	next.state.subagents = next.state.subagents[:len(next.state.subagents)-closed]
	return next, events
}

func (s stream) finishSubagents() (stream, []aguievents.Event) {
	next := s.clone()
	var events []aguievents.Event
	for index := len(next.state.subagents) - 1; index >= 0; index-- {
		events = append(events, aguievents.NewSubagentFinishedEvent(next.state.subagents[index].id))
	}
	next.state.subagents = nil
	return next, events
}

func (s stream) failSubagents(message, code string) (stream, []aguievents.Event) {
	next := s.clone()
	var events []aguievents.Event
	for index := len(next.state.subagents) - 1; index >= 0; index-- {
		events = append(events, aguievents.NewSubagentErrorEvent(
			next.state.subagents[index].id,
			message,
			aguievents.WithSubagentErrorCode(code),
		))
	}
	next.state.subagents = nil
	return next, events
}

func (s stream) parentID(branch string) string {
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
