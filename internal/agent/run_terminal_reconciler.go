package agent

import (
	"context"
	"time"

	"google.golang.org/adk/v2/session"
)

func (r *Runtime) observeRunTerminalReconciliation(status string, duration time.Duration) {
	if r.config.Metrics != nil {
		r.config.Metrics.ObserveRunTerminalReconciliation(status, duration)
	}
}

func (r *Runtime) observePendingTerminalRuns(delta int) {
	if r.config.Metrics != nil {
		r.config.Metrics.ObservePendingTerminalRuns(delta)
	}
}

// terminalReconciliationAttempts bounds one Run-owned background retry. The
// run remains active if every attempt fails; startup recovery is the final
// append-only reconciliation boundary.
const terminalReconciliationAttempts = 5

// reconcileRunTerminalEvidence retries the shared terminal-evidence transaction
// after a Run request has returned an infrastructure failure. It never reopens
// terminal child lifecycles: closeRunEvidence is idempotent at the evidence-ID
// boundary and remaining pending children are compensated on the first pass.
func (r *Runtime) reconcileRunTerminalEvidence(
	handle AuditAttemptHandle,
	scope *runExecution,
	cause error,
	outcome auditOutcome,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	delay := 25 * time.Millisecond
	started := r.config.Now()

	for attempt := 0; attempt < terminalReconciliationAttempts; attempt++ {
		claimed, wasPending := r.runs.claimClose(string(scope.identity.RunID))
		if !claimed {
			return
		}
		err := r.closeRunEvidence(ctx, scope, cause, outcome)
		if err == nil {
			if wasPending {
				r.observePendingTerminalRuns(-1)
			}
			r.observeRunTerminalReconciliation("succeeded", r.config.Now().Sub(started))
			r.completeTerminalRunCleanup(ctx, handle, scope)
			return
		}
		r.runs.failClose(string(scope.identity.RunID))
		if attempt < terminalReconciliationAttempts-1 {
			select {
			case <-ctx.Done():
				r.observeRunTerminalReconciliation("stopped", r.config.Now().Sub(started))
				r.config.Logger.WarnContext(ctx, "agent_run_terminal_reconciliation_stopped",
					"run_id", string(scope.identity.RunID),
					"error", runtimeError(err).Error(),
				)
				return
			case <-time.After(delay):
			}
			r.observeRunTerminalReconciliation("retry_scheduled", r.config.Now().Sub(started))
			delay *= 2
		}
	}

	r.observeRunTerminalReconciliation("failed", r.config.Now().Sub(started))
	r.config.Logger.ErrorContext(ctx, "agent_run_terminal_reconciliation_failed",
		"run_id", string(scope.identity.RunID),
		"error", runtimeError(cause).Error(),
	)
}

// completeTerminalRunCleanup releases attempt bookkeeping and run-owned working
// state only after the terminal audit fact is durably appended.
func (r *Runtime) completeTerminalRunCleanup(
	ctx context.Context,
	handle AuditAttemptHandle,
	scope *runExecution,
) {
	r.callAuditor.forget(handle)
	if scope.releaseRun != nil {
		scope.releaseRun()
	}
	if deleteErr := r.ephemeralSessions.Delete(ctx, &session.DeleteRequest{
		AppName:   r.config.AppName,
		UserID:    scope.identity.UserID,
		SessionID: runSessionID(string(scope.identity.RunID)),
	}); deleteErr != nil {
		r.config.Logger.WarnContext(ctx, "agent_run_session_cleanup_failed",
			"run_id", string(scope.identity.RunID),
			"error", runtimeError(deleteErr).Error(),
		)
	}
	r.runs.finish(string(scope.identity.RunID))
}
