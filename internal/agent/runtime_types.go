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
	ContextID string
	AttemptID domain.AttemptID
}

func (h AuditAttemptHandle) normalize() AuditAttemptHandle {
	return AuditAttemptHandle{
		ContextID: strings.TrimSpace(h.ContextID),
		AttemptID: domain.AttemptID(strings.TrimSpace(string(h.AttemptID))),
	}
}

func (h AuditAttemptHandle) validate() error {
	if h.ContextID == "" {
		return domain.NewError(domain.CodeAuditSessionRequired, "audit protocol context identifier is required", domain.ErrInvalidInput)
	}
	if h.AttemptID == "" {
		return domain.NewError(domain.CodeRunIDRequired, "audit attempt identifier is required", domain.ErrInvalidInput)
	}
	return nil
}

func (s RunAuditIdentity) normalize() RunAuditIdentity {
	return RunAuditIdentity{
		AttemptID: domain.AttemptID(strings.TrimSpace(string(s.AttemptID))),
		ThreadID:  strings.TrimSpace(s.ThreadID),
		UserID:    strings.TrimSpace(s.UserID),
		Protocol:  s.Protocol,
		TaskID:    strings.TrimSpace(s.TaskID),
		MessageID: strings.TrimSpace(s.MessageID),
	}
}

func (s RunAuditIdentity) runIdentity() (execution.RunIdentity, error) {
	identity := execution.RunIdentity{
		RunID:    domain.ID(s.AttemptID),
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
	AttemptID domain.AttemptID
	ThreadID  string
	UserID    string
	Protocol  string
	TaskID    string
	MessageID string
}
