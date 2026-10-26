package agent

import (
	"strings"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"github.com/ml8s/liki-agent-runtime/internal/execution"
)

func (s RunAuditIdentity) normalize() RunAuditIdentity {
	return RunAuditIdentity{
		RunID:    strings.TrimSpace(s.RunID),
		ThreadID: strings.TrimSpace(s.ThreadID),
		UserID:   strings.TrimSpace(s.UserID),
		Protocol: s.Protocol,
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
	RunID    string
	ThreadID string
	UserID   string
	Protocol string
}
