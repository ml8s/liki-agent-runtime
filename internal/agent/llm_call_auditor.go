package agent

import agentdeployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"context"
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"google.golang.org/adk/v2/agent"
)

type llmCallAuditor struct {
	mu       sync.Mutex
	recorder audit.Recorder
	metrics  Metrics
	now      func() time.Time
	provider string
	agents   map[string]*agentdeployment.ResolvedAgentDefinition
	scopes   map[string]*runExecution
}

func newLLMCallAuditor(recorder audit.Recorder, metrics Metrics, provider string, deployment *agentdeployment.AgentDeployment, now func() time.Time) *llmCallAuditor {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	ledger := &llmCallAuditor{
		recorder: recorder,
		metrics:  metrics,
		provider: provider,
		now:      now,
		agents:   make(map[string]*agentdeployment.ResolvedAgentDefinition, len(deployment.Spec.Agents)),
		scopes:   make(map[string]*runExecution),
	}
	for index := range deployment.Spec.Agents {
		ledger.agents[deployment.Spec.Agents[index].Name] = &deployment.Spec.Agents[index]
	}
	return ledger
}

func (l *llmCallAuditor) begin(sessionID string, scope *runExecution) bool {
	l.mu.Lock()
	if _, exists := l.scopes[sessionID]; exists {
		l.mu.Unlock()
		return false
	}
	l.scopes[sessionID] = scope
	l.mu.Unlock()
	return true
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

func (l *llmCallAuditor) forget(sessionID string) {
	l.mu.Lock()
	delete(l.scopes, sessionID)
	l.mu.Unlock()
}

func (l *llmCallAuditor) end(sessionID string, runErr error) (bool, error) {
	scope, exists := l.scope(sessionID)
	if !exists {
		return false, nil
	}
	err := l.closeActive(scope, runErr)
	l.forget(sessionID)
	return true, err
}

func (l *llmCallAuditor) scope(sessionID string) (*runExecution, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	scope, exists := l.scopes[sessionID]
	return scope, exists
}

func (l *llmCallAuditor) failActive(scope *runExecution, err error, finishedAt time.Time) error {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	var firstErr error
	for callbackKey, runtime := range scope.activeCalls {
		call := domain.LLMCall{
			ID: runtime.id, RunID: scope.identity.RunID, ThreadID: scope.identity.ThreadID,
			UserID: scope.identity.UserID, AgentName: runtime.agentName, Model: runtime.model,
			Provider: l.provider, Status: domain.LLMCallFailed,
			PromptVersion: runtime.instructionDigest,
			StartedAt:     runtime.startedAt, FinishedAt: finishedAt,
		}
		call = domain.FailLLMCall(call, err, finishedAt)
		call = domain.NormalizeLLMCall(call)
		call = l.withAgent(call, call.AgentName)
		recordErr := l.recordCall(context.Background(), scope, &call, audit.EventLLMCallFailed, audit.StatusFailed)
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

func (l *llmCallAuditor) withAgent(call domain.LLMCall, name string) domain.LLMCall {
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
	call := domain.LLMCall{
		ID:                runtime.id,
		RunID:             scope.identity.RunID,
		ThreadID:          scope.identity.ThreadID,
		UserID:            scope.identity.UserID,
		AgentName:         runtime.agentName,
		Model:             runtime.model,
		Provider:          l.provider,
		Status:            domain.LLMCallRunning,
		PromptVersion:     scope.instructionDigest,
		DefinitionName:    scope.definitionName,
		DefinitionVersion: scope.definitionVersion,
		DefinitionDigest:  scope.definitionDigest,
		StartedAt:         runtime.startedAt,
	}
	call = l.withAgent(domain.NormalizeLLMCall(call), call.AgentName)
	if err := domain.ValidateLLMCall(call); err != nil {
		return err
	}
	return l.recordCall(context.WithoutCancel(ctx), scope, &call, audit.EventLLMCallStarted, audit.StatusRunning)
}

func (l *llmCallAuditor) recordCall(ctx context.Context, scope *runExecution, call *domain.LLMCall, eventType audit.EventType, status audit.Status) error {
	if l.recorder == nil {
		return domain.NewError(domain.CodeAuditRecorderMissing, "audit recorder is required", nil)
	}
	event := scopedEvent(scope)
	event.ID = call.ID + ":" + string(eventType)
	event.Type = eventType
	event.AgentName = call.AgentName
	event.AgentVersion = call.AgentVersion
	event.AgentDefinitionDigest = call.AgentDefinitionDigest
	event.Model = call.Model
	event.Provider = call.Provider
	event.Status = status
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
