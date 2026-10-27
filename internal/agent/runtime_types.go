package agent

import (
	"strings"

	"github.com/ml8s/agent-runtime/internal/domain"
	"github.com/ml8s/agent-runtime/internal/execution"
)

// AuditAttemptHandle separates the identifiers that A2A intentionally keeps
// distinct: the ADK session is owned by the protocol ContextID, while each
// execution and its audit lifecycle is owned by a unique attempt ID.
type AuditAttemptHandle struct {
	ADKSessionID string
	AttemptID    string
}

func (h AuditAttemptHandle) normalize() AuditAttemptHandle {
	return AuditAttemptHandle{
		ADKSessionID: strings.TrimSpace(h.ADKSessionID),
		AttemptID:    strings.TrimSpace(h.AttemptID),
	}
}

func (h AuditAttemptHandle) validate() error {
	if h.ADKSessionID == "" {
		return domain.NewError(domain.CodeAuditSessionRequired, "audit ADK session identifier is required", domain.ErrInvalidInput)
	}
	if h.AttemptID == "" {
		return domain.NewError(domain.CodeRunIDRequired, "audit attempt identifier is required", domain.ErrInvalidInput)
	}
	return nil
}

func (s RunAuditIdentity) normalize() RunAuditIdentity {
	return RunAuditIdentity{
		RunID:     strings.TrimSpace(s.RunID),
		ThreadID:  strings.TrimSpace(s.ThreadID),
		UserID:    strings.TrimSpace(s.UserID),
		Protocol:  s.Protocol,
		TaskID:    strings.TrimSpace(s.TaskID),
		MessageID: strings.TrimSpace(s.MessageID),
	}
}

func (s RunAuditIdentity) runIdentity() (execution.RunIdentity, error) {
	identity := execution.RunIdentity{
		RunID:    domain.ID(s.RunID),
		ThreadID: domain.ID(s.ThreadID),
		UserID:   s.UserID,
	}
	if err := identity.Validate(); err != nil {
		return execution.RunIdentity{}, err
	}
	return identity, nil
}

// RunAuditIdentity identifies an externally driven ADK execution for the LLM
// audit ledger. It does not create a second execution path.
type RunAuditIdentity struct {
	RunID     string
	ThreadID  string
	UserID    string
	Protocol  string
	TaskID    string
	MessageID string
}
