package execution

import (
	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

// RunIdentity identifies one execution aggregate. ThreadID and UserID are
// caller-owned correlation values, not parts of aggregate identity.
type RunIdentity struct {
	RunID    domain.ID
	ThreadID domain.ID
	UserID   string
}

// Validate enforces the identity fields required by every run lifecycle.
func (i RunIdentity) Validate() error {
	switch {
	case i.RunID == "":
		return domain.NewError(domain.CodeRunIDRequired, "run id is required", domain.ErrInvalidInput)
	case !domain.ValidIdentifier(string(i.RunID)):
		return domain.NewError(domain.CodeRunIDInvalid, "run id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	case i.ThreadID == "":
		return domain.NewError(domain.CodeThreadIDRequired, "thread id is required", domain.ErrInvalidInput)
	case !domain.ValidIdentifier(string(i.ThreadID)):
		return domain.NewError(domain.CodeThreadIDInvalid, "thread id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	case i.UserID == "":
		return domain.NewError(domain.CodeUserIDRequired, "user id is required", domain.ErrInvalidInput)
	case !domain.ValidIdentifier(i.UserID):
		return domain.NewError(domain.CodeUserIDInvalid, "user id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	default:
		return nil
	}
}

// RunProvenance records where and how an execution entered the runtime. These
// fields are evidence metadata, not aggregate identity.
type RunProvenance struct {
	Protocol  string
	TraceID   string
	SpanID    string
	TaskID    string
	MessageID string
}
