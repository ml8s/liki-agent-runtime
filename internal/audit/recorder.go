package audit

import (
	"context"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

// Recorder persists immutable audit facts. Implementations must treat Record
// as append-only: an event must never overwrite or remove a prior event.
type Recorder interface {
	Record(ctx context.Context, event *Event) error
}

// RunExistenceChecker is an optional recorder capability for durable RunID
// idempotency after process restart or in-memory registry eviction.
type RunExistenceChecker interface {
	RunExists(ctx context.Context, runID domain.ID) (bool, error)
}

// InterruptionRecoverer is an optional recorder capability for closing audit
// lifecycles left running by a hard process interruption. Recovery is strictly
// append-only: existing evidence is never rewritten.
type InterruptionRecoverer interface {
	RecoverInterrupted(ctx context.Context, now time.Time) error
}
