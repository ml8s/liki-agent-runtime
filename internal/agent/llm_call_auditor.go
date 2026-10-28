package agent

import (
	"context"
	"time"

	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
	"github.com/ml8s/agent-runtime/internal/execution"
	"google.golang.org/adk/v2/agent"
)

type llmCallAuditor struct {
	recorder audit.Recorder
	metrics  execution.Metrics
	now      func() time.Time
	provider string
	agents   map[string]*agentdeployment.ResolvedAgentDefinition

	// attempts owns every externally driven attempt index. Callbacks resolve
	// scopes only through it; this type keeps no parallel attempt state.
	attempts *attemptRegistry
}

func newLLMCallAuditor(recorder audit.Recorder, metrics execution.Metrics, provider string, deployment *agentdeployment.AgentDeployment, now func() time.Time) *llmCallAuditor {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	ledger := &llmCallAuditor{
		recorder: recorder,
		metrics:  metrics,
		provider: provider,
		now:      now,
		agents:   make(map[string]*agentdeployment.ResolvedAgentDefinition, len(deployment.Spec.Agents)),
		attempts: newAttemptRegistry(),
	}
	for index := range deployment.Spec.Agents {
		ledger.agents[deployment.Spec.Agents[index].Name] = &deployment.Spec.Agents[index]
	}
	return ledger
}

func (l *llmCallAuditor) begin(handle AuditAttemptHandle, scope *runExecution) bool {
	handle = handle.normalize()
	if err := handle.validate(); err != nil {
		return false
	}
	return l.attempts.begin(
		string(handle.AttemptID),
		handle.ContextID,
		scope,
	)
}

// closeActive fails every in-flight model call for the run and leaves the run
// scope in place, so a caller can append terminal evidence before releasing it.
// A nil run error represents an interruption.
func (l *llmCallAuditor) closeActive(scope *runExecution, runErr error) error {
	if scope == nil {
		return nil
	}
	failure := runErr
	if failure == nil {
		failure = domain.NewError(domain.CodeRuntimeInterrupted, "runtime stopped before the LLM call finished", nil)
	}
	return l.failActive(scope, failure, l.now())
}

func (l *llmCallAuditor) forget(handle AuditAttemptHandle) {
	handle = handle.normalize()
	l.attempts.forgetContext(handle.ContextID)
}

func (l *llmCallAuditor) forgetAttempt(handle AuditAttemptHandle) {
	handle = handle.normalize()
	l.attempts.unbindAttempt(string(handle.AttemptID))
}

func (l *llmCallAuditor) end(handle AuditAttemptHandle, runErr error) {
	handle = handle.normalize()
	scope, exists := l.scope(string(handle.AttemptID))
	if !exists {
		return
	}
	_ = l.closeActive(scope, runErr)
	l.forgetAttempt(handle)
}

func (l *llmCallAuditor) scope(sessionID string) (*runExecution, bool) {
	return l.attempts.lookup(sessionID)
}

// unbindInvocations drops only the ADK invocation bridge of a finished
// invocation; the attempt stays registered for its protocol context.
func (l *llmCallAuditor) unbindInvocations(attemptID string) {
	l.attempts.unbindInvocations(attemptID)
}

// bindInvocation records the ADK invocation that belongs to an externally
// driven attempt. A2A ContextID can contain multiple invocations; model, tool,
// and delegation callbacks resolve through this bridge.
type auditAttemptKey struct{}

// WithAuditAttempt attaches the externally driven audit attempt to the ADK
// execution context. The attempt plugin bridges it to ADK InvocationID.
func WithAuditAttempt(ctx context.Context, attemptID string) context.Context {
	return context.WithValue(ctx, auditAttemptKey{}, attemptID)
}

func (l *llmCallAuditor) bindInvocation(sessionID, invocationID, attemptID string) {
	l.attempts.bindInvocation(sessionID, invocationID, attemptID)
}

func (l *llmCallAuditor) auditScope(ctx agent.Context) (*runExecution, bool) {
	if attemptID, ok := ctx.Value(auditAttemptKey{}).(string); ok && attemptID != "" {
		return l.scope(attemptID)
	}
	if sessionID, invocationID := ctx.SessionID(), ctx.InvocationID(); sessionID != "" && invocationID != "" {
		if attemptID, ok := l.attempts.attemptForInvocation(sessionID, invocationID); ok {
			return l.scope(attemptID)
		}
	}
	return l.scope(ctx.SessionID())
}

func (l *llmCallAuditor) failActive(scope *runExecution, err error, finishedAt time.Time) error {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	var firstErr error
	for callbackKey, runtime := range scope.activeCalls {
		call := execution.LLMCall{
			ID: runtime.id, RunID: scope.identity.RunID, ThreadID: scope.identity.ThreadID,
			UserID: scope.identity.UserID, AgentName: runtime.agentName, Model: runtime.model,
			Provider: l.provider, Status: execution.LLMCallFailed,
			PromptVersion: runtime.instructionDigest,
			StartedAt:     runtime.startedAt, FinishedAt: finishedAt,
		}
		call = execution.FailLLMCall(call, err, finishedAt)
		call = execution.NormalizeLLMCall(call)
		call = l.withAgent(call, call.AgentName)
		recordErr := l.recordCall(context.Background(), scope, &call, audit.EventLLMCallFailed)
		if recordErr == nil {
			delete(scope.activeCalls, callbackKey)
		} else if firstErr == nil {
			firstErr = recordErr
		}
	}
	return firstErr
}

func (l *llmCallAuditor) reconcile(scope *runExecution, cause error) error {
	if scope == nil {
		return nil
	}
	return l.failActive(scope, cause, l.now())
}

func (l *llmCallAuditor) withAgent(call execution.LLMCall, name string) execution.LLMCall {
	definition, ok := l.agents[name]
	if !ok {
		return call
	}
	call.AgentName = definition.Name
	call.AgentVersion = definition.Version
	call.AgentDefinitionDigest = definition.Digest
	call.PromptVersion = definition.InstructionDigest
	return call
}

// recordCallStarted appends the started fact for a model call that ADK reported
// without a matching BeforeModel callback, preserving the audit rule that
// every model call has started and terminal evidence.
func (l *llmCallAuditor) recordCallStarted(ctx agent.Context, scope *runExecution, runtime llmCallRuntime) error {
	call := execution.LLMCall{
		ID:                runtime.id,
		RunID:             scope.identity.RunID,
		ThreadID:          scope.identity.ThreadID,
		UserID:            scope.identity.UserID,
		AgentName:         runtime.agentName,
		Model:             runtime.model,
		Provider:          l.provider,
		Status:            execution.LLMCallRunning,
		PromptVersion:     scope.instructionDigest,
		DefinitionName:    scope.definitionName,
		DefinitionVersion: scope.definitionVersion,
		DefinitionDigest:  scope.definitionDigest,
		StartedAt:         runtime.startedAt,
	}
	call = l.withAgent(execution.NormalizeLLMCall(call), call.AgentName)
	if err := execution.ValidateLLMCall(call); err != nil {
		return err
	}
	return l.recordCall(context.WithoutCancel(ctx), scope, &call, audit.EventLLMCallStarted)
}

func (l *llmCallAuditor) recordCall(ctx context.Context, scope *runExecution, call *execution.LLMCall, eventType audit.EventType) error {
	if l.recorder == nil {
		return domain.NewError(domain.CodeAuditRecorderMissing, "audit recorder is required", nil)
	}
	event := scopedTerminalEvent(ctx, scope, eventType)
	event.ID = call.ID + ":" + string(eventType)
	event.AgentName = call.AgentName
	event.AgentVersion = call.AgentVersion
	event.AgentDefinitionDigest = call.AgentDefinitionDigest
	event.Model = call.Model
	event.Provider = call.Provider
	event.DurationMS = call.DurationMS
	event.ErrorCode = call.ErrorCode
	event.ErrorMessage = call.ErrorMessage
	event.TraceID = traceIDFromContext(ctx)
	event.SpanID = spanIDFromContext(ctx)
	if event.TraceID == "" && scope.provenance.TraceID != "" {
		// Reconciliation paths may lack a live span context (for example
		// failActive). Fall back to the run-scoped trace so reconciled
		// terminal events stay correlatable (DOMAIN invariant 9).
		event.TraceID = scope.provenance.TraceID
		event.SpanID = scope.provenance.SpanID
	}
	event.Payload = map[string]any{
		"prompt_version":    call.PromptVersion,
		"prompt_tokens":     call.PromptTokens,
		"completion_tokens": call.CompletionTokens,
		"thought_tokens":    call.ThoughtTokens,
		"total_tokens":      call.TotalTokens,
	}
	if eventType == audit.EventLLMCallStarted {
		event.OccurredAt = call.StartedAt
	} else {
		event.OccurredAt = call.FinishedAt
	}
	event, err := audit.ValidateEvent(event)
	if err != nil {
		return err
	}
	return l.recorder.Record(ctx, &event)
}
