package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
	"google.golang.org/adk/v2/session"
)

type runLifecycle struct {
	mu         sync.Mutex
	active     map[string]struct{}
	completed  map[string]struct{}
	order      []string
	orderStart int
}

func (r *Runtime) acquireRun(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		r.observeRunRejection("cancelled")
		return nil, runtimeError(err)
	}
	select {
	case r.runSlots <- struct{}{}:
		return func() { <-r.runSlots }, nil
	default:
		r.observeRunRejection("busy")
		return nil, domain.NewError(domain.CodeRuntimeBusy, "runtime is at its concurrent run limit", ctx.Err())
	}
}

func (r *Runtime) observeRunRejection(reason string) {
	if r.config.Metrics != nil {
		r.config.Metrics.ObserveRunRejection(reason)
	}
}

func newRunLifecycle() *runLifecycle {
	return &runLifecycle{
		active:    make(map[string]struct{}),
		completed: make(map[string]struct{}),
	}
}

func (l *runLifecycle) begin(runID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.active[runID]; exists {
		return false
	}
	if _, exists := l.completed[runID]; exists {
		return false
	}
	l.active[runID] = struct{}{}
	return true
}

func (l *runLifecycle) finish(runID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.active, runID)
	l.completed[runID] = struct{}{}
	l.order = append(l.order, runID)
	// Ring eviction keeps duplicate-run detection bounded without repeatedly
	// reslicing the backing array, which would grow forever.
	for len(l.order)-l.orderStart > maxCompletedRunIDs {
		delete(l.completed, l.order[l.orderStart])
		l.order[l.orderStart] = ""
		l.orderStart++
	}
	if l.orderStart == len(l.order) {
		l.order = l.order[:0]
		l.orderStart = 0
	}
}

// BeginAuditRun attaches protocol identity to model callbacks when an official
// protocol executor drives the shared ADK runtime directly.
func (r *Runtime) BeginAuditRun(ctx context.Context, handle AuditAttemptHandle, scope RunAuditIdentity) error {
	scope = scope.normalize()
	handle = handle.normalize()
	identity, err := scope.runIdentity()
	if err != nil {
		return err
	}
	if err := r.ensureDurableRunID(ctx, string(scope.AttemptID)); err != nil {
		return err
	}
	if err := handle.validate(); err != nil {
		return err
	}
	if !r.runs.begin(string(scope.AttemptID)) {
		return domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
	}
	releaseRun, err := r.acquireRun(ctx)
	if err != nil {
		r.runs.finish(string(scope.AttemptID))
		return err
	}
	ledgerScope := &runExecution{
		identity: identity,
		provenance: execution.RunProvenance{
			Protocol:  scope.Protocol,
			TraceID:   traceIDFromContext(ctx),
			SpanID:    spanIDFromContext(ctx),
			TaskID:    scope.TaskID,
			MessageID: scope.MessageID,
		},
		attemptKey:        string(scope.AttemptID),
		sessionID:         handle.ContextID,
		taskID:            scope.TaskID,
		model:             r.entrypoint.EffectiveModel(r.config.Model),
		instructionDigest: r.entrypoint.InstructionDigest,
		definitionName:    r.definition.Metadata.Name,
		definitionVersion: r.definition.Metadata.Version,
		definitionDigest:  r.definition.Digest,
		startedAt:         r.config.Now(),
		releaseRun:        releaseRun,
		activeCalls:       make(map[string]llmCallRuntime),
	}
	if !r.callAuditor.begin(handle, ledgerScope) {
		releaseRun()
		r.runs.finish(string(scope.AttemptID))
		return domain.NewError(domain.CodeAuditSessionActive, "an audit run is already active for this session", domain.ErrInvalidInput)
	}
	event := r.runEvent(ctx, ledgerScope, audit.EventRunStarted, audit.StatusRunning, nil)
	if err := r.recordAuditWithRetry(ctx, &event); err != nil {
		r.runs.finish(string(scope.AttemptID))
		r.callAuditor.end(handle, err)
		_ = r.delegationAuditor.FailPending(ctx, ledgerScope)
		_ = r.toolAuditor.FailPending(ctx, ledgerScope)
		releaseRun()
		return err
	}
	return nil
}

// AuditEndOption customizes closure of one externally driven audit attempt.
type AuditEndOption func(*auditEndOptions)

type auditEndOptions struct {
	paused          bool
	canceled        bool
	preserveSession bool
}

// WithCanceledAttempt records a standard cancellation as cancellation evidence
// rather than a generic failure.
func WithCanceledAttempt() AuditEndOption {
	return func(o *auditEndOptions) { o.canceled = true }
}

// WithPausedAttempt records a non-terminal pause (for example A2A
// input-required) instead of a terminal success or failure.
func WithPausedAttempt() AuditEndOption {
	return func(o *auditEndOptions) { o.paused = true }
}

// WithPreservedSession keeps the ADK session for protocol continuation. A2A
// ContextIDs own session lifetime; audit completion does not imply context
// lifetime.
func WithPreservedSession() AuditEndOption {
	return func(o *auditEndOptions) { o.preserveSession = true }
}

// EndAuditRun completes an externally driven audit lifecycle exactly once.
//
// The run scope is released only after the terminal run event is durably
// appended. A transient append failure therefore returns without releasing the
// scope or the concurrency slot, so the executor's cleanup callback retries the
// terminal append rather than stranding an orphaned started event. Startup
// recovery remains the last resort if the process exits before a retry succeeds.
func (r *Runtime) EndAuditRun(ctx context.Context, handle AuditAttemptHandle, runErr error, options ...AuditEndOption) error {
	var opts auditEndOptions
	for _, option := range options {
		option(&opts)
	}
	ctx = context.WithoutCancel(ctx)
	handle = handle.normalize()
	if err := handle.validate(); err != nil {
		return err
	}
	scope, existed := r.callAuditor.scope(string(handle.AttemptID))
	if !existed {
		return nil
	}

	// Terminal evidence transaction (shared with Run; see run_evidence.go):
	// close in-flight → compensate → terminal event. On append failure this
	// returns without releasing scope or state; the executor's cleanup
	// callback owns the retry. Causal and compensation errors are already
	// encoded as the terminal event's failure reason.
	outcome := auditOutcomeCompleted
	switch {
	case opts.canceled:
		outcome = auditOutcomeCanceled
	case opts.paused:
		outcome = auditOutcomePaused
	case runErr != nil:
		outcome = auditOutcomeFailed
	}
	if err := r.closeRunEvidence(ctx, scope, runErr, outcome); err != nil {
		return err
	}

	// Protocol sessions can outlive an attempt. Only a runtime that owns a
	// run-scoped session may delete it when the attempt ends.
	if !opts.preserveSession {
		if deleteErr := r.sessions.Delete(ctx, &session.DeleteRequest{
			AppName:   r.config.AppName,
			UserID:    scope.identity.UserID,
			SessionID: handle.ContextID,
		}); deleteErr != nil {
			r.config.Logger.WarnContext(ctx, "external_run_session_cleanup_failed",
				"run_id", string(scope.identity.RunID),
				"error", runtimeError(deleteErr).Error(),
			)
		}
	}
	r.callAuditor.forgetAttempt(handle)
	if scope.releaseRun != nil {
		scope.releaseRun()
	}
	r.runs.finish(string(scope.identity.RunID))
	return nil
}

// runEvent is the single constructor for run-lifecycle audit events, shared by
// the protocol-driven Run path and the externally driven BeginAuditRun /
// EndAuditRun path.
func (r *Runtime) runEvent(
	ctx context.Context,
	scope *runExecution,
	eventType audit.EventType,
	status audit.Status,
	cause error,
) audit.Event {
	event := scopedEvent(scope)
	event.ID = string(scope.identity.RunID) + ":" + string(eventType)
	event.Type = eventType
	event.OccurredAt = r.config.Now()
	event.AgentName = r.entrypoint.Name
	event.AgentVersion = r.entrypoint.Version
	event.AgentDefinitionDigest = r.entrypoint.Digest
	event.Model = r.entrypoint.EffectiveModel(r.config.Model)
	event.Provider = r.config.Provider
	event.Status = status
	event.DurationMS = r.config.Now().Sub(scope.startedAt).Milliseconds()
	event.TraceID = traceIDFromContext(ctx)
	event.SpanID = spanIDFromContext(ctx)
	if cause != nil {
		event.ErrorCode, event.ErrorMessage = errorFields(cause)
	}
	return event
}

// errorFields maps an arbitrary error to stable audit error fields.
func errorFields(cause error) (code, message string) {
	runtimeErr := runtimeError(cause)
	var domainErr *domain.Error
	if errors.As(runtimeErr, &domainErr) {
		return domainErr.Code, domainErr.Message
	}
	return domain.CodeRuntimeFailed, runtimeErr.Error()
}

// runSessionID returns the run-scoped ADK session identity for a run:
// one session per run, deleted at terminal state, never carrying cross-run
// history (durable history belongs to the caller).
func runSessionID(runID string) string {
	return "run:" + runID
}

// normalizeRunRequest trims leading/trailing whitespace from identifiers,
// protocol, and user message. Every downstream consumer (audit, session,
// model input) receives the normalized values.
func normalizeRunRequest(request execution.RunRequest) execution.RunRequest {
	request.RunID = strings.TrimSpace(request.RunID)
	request.ThreadID = strings.TrimSpace(request.ThreadID)
	request.UserID = strings.TrimSpace(request.UserID)
	request.Protocol = strings.TrimSpace(request.Protocol)
	request.UserMessage = strings.TrimSpace(request.UserMessage)
	return request
}

// validateRunRequest enforces the run input invariants: all three
// identifiers must be present and bounded printable, the user message must
// not be empty, and history must not exceed its limit.
func validateRunRequest(request execution.RunRequest) error {
	if request.UserMessage == "" {
		return domain.NewError(domain.CodeUserMessageRequired, "user message is required", domain.ErrInvalidInput)
	}
	if err := request.Identity().Validate(); err != nil {
		return err
	}
	if len(request.History) > execution.MaxHistoryMessages {
		return domain.NewError(domain.CodeHistoryInvalid, fmt.Sprintf("conversation history exceeds %d messages", execution.MaxHistoryMessages), domain.ErrInvalidInput)
	}
	return nil
}
