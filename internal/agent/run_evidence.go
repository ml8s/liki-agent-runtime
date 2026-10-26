package agent

// Run terminal evidence transaction: this file is the single implementation
// of the aggregate invariant "terminal evidence appended exactly once, in
// order". Run's deferred finalizer and EndAuditRun share closeRunEvidence.

import (
	"context"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
)

// closeRunEvidence executes the run terminal-evidence transaction in order:
//  1. Close in-flight model calls (reconcile once on failure);
//  2. Compensate pending delegation and tool audits;
//  3. Append the terminal event with the causal error (preferred).
//
// Causal and compensation errors are encoded as the terminal event's failure
// reason. The return value only indicates whether the terminal event was
// durably appended — on failure the caller decides whether to retry or hand
// over to startup recovery, and must not release any process state before
// then. Run and EndAuditRun share this single implementation.
func (r *Runtime) closeRunEvidence(ctx context.Context, scope *runExecution, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if ledgerErr := r.callAuditor.closeActive(scope, runtimeError(cause)); ledgerErr != nil {
		if reconcileErr := r.callAuditor.reconcile(scope, ledgerErr); reconcileErr != nil && cause == nil {
			cause = reconcileErr
		}
	}
	if err := r.delegationAuditor.FailPending(ctx, scope); err != nil && cause == nil {
		cause = err
	}
	if err := r.toolAuditor.FailPending(ctx, scope); err != nil && cause == nil {
		cause = err
	}
	eventType, status := audit.EventRunCompleted, audit.StatusSucceeded
	if cause != nil {
		eventType, status = audit.EventRunFailed, audit.StatusFailed
	}
	return r.recordRunAudit(ctx, scope, eventType, status, cause)
}
