package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type llmCallAuditor struct {
	mu       sync.Mutex
	recorder audit.Recorder
	metrics  Metrics
	now      func() time.Time
	provider string
	agents   map[string]*AgentDefinition
	scopes   map[string]*runScope
}

type runScope struct {
	mu                sync.Mutex
	runID             domain.ID
	threadID          domain.ID
	userID            string
	startedAt         time.Time
	protocol          string
	model             string
	instructionDigest string
	traceID           string
	spanID            string
	releaseRun        func()
	definitionName    string
	definitionVersion string
	definitionDigest  string
	nextCall          int
	activeCalls       map[string]llmCallRuntime

	llmCalls         int
	promptTokens     int
	completionTokens int
	thoughtTokens    int
}

// usage returns the aggregated model usage recorded for this run so far.
func (s *runScope) usage() RunUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RunUsage{
		LLMCalls:         s.llmCalls,
		PromptTokens:     s.promptTokens,
		CompletionTokens: s.completionTokens,
		ThoughtTokens:    s.thoughtTokens,
	}
}

type llmCallRuntime struct {
	id                string
	model             string
	agentName         string
	instructionDigest string
	startedAt         time.Time
}

func newLLMCallAuditor(recorder audit.Recorder, metrics Metrics, provider string, deployment *Deployment, now func() time.Time) *llmCallAuditor {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	ledger := &llmCallAuditor{
		recorder: recorder,
		metrics:  metrics,
		provider: provider,
		now:      now,
		agents:   make(map[string]*AgentDefinition, len(deployment.Spec.Agents)),
		scopes:   make(map[string]*runScope),
	}
	for index := range deployment.Spec.Agents {
		ledger.agents[deployment.Spec.Agents[index].Name] = &deployment.Spec.Agents[index]
	}
	return ledger
}

func (l *llmCallAuditor) begin(sessionID string, scope *runScope) bool {
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
func (l *llmCallAuditor) closeActive(scope *runScope, runErr error) error {
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

func (l *llmCallAuditor) scope(sessionID string) (*runScope, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	scope, exists := l.scopes[sessionID]
	return scope, exists
}

func (l *llmCallAuditor) beforeModel(ctx agent.Context, request *model.LLMRequest) (*model.LLMResponse, error) {
	if request == nil {
		return nil, domain.NewError(domain.CodeLLMRequestInvalid, "LLM request is required", domain.ErrInvalidInput)
	}
	scope, ok := l.scope(ctx.SessionID())
	if !ok || l.recorder == nil {
		return nil, domain.NewError(
			domain.CodeAuditScopeMissing,
			"LLM callback has no active audit scope",
			domain.ErrInvalidInput,
		)
	}
	callbackKey, err := llmCallbackKey(ctx)
	if err != nil {
		return nil, err
	}
	modelName := request.Model
	if modelName == "" {
		modelName = scope.model
	}
	scope.mu.Lock()
	scope.nextCall++
	callSeq := scope.nextCall
	scope.mu.Unlock()
	callID := fmt.Sprintf("%s/%s/%d", ctx.InvocationID(), modelName, callSeq)
	startedAt := l.now()
	call := &domain.LLMCall{
		ID:                callID,
		RunID:             scope.runID,
		ThreadID:          scope.threadID,
		UserID:            scope.userID,
		AgentName:         ctx.AgentName(),
		Model:             modelName,
		Provider:          l.provider,
		Status:            domain.LLMCallRunning,
		PromptVersion:     scope.instructionDigest,
		DefinitionName:    scope.definitionName,
		DefinitionVersion: scope.definitionVersion,
		DefinitionDigest:  scope.definitionDigest,
		StartedAt:         startedAt,
	}
	call.Normalize()
	l.applyAgent(call, call.AgentName)
	if err := call.Validate(); err != nil {
		return nil, err
	}
	if err := l.recordCall(ctx, scope, call, audit.EventLLMCallStarted, audit.StatusRunning); err != nil {
		return nil, err
	}
	runtime := llmCallRuntime{
		id:                callID,
		model:             modelName,
		agentName:         call.AgentName,
		instructionDigest: call.PromptVersion,
		startedAt:         startedAt,
	}
	scope.mu.Lock()
	scope.activeCalls[callbackKey] = runtime
	scope.mu.Unlock()
	return nil, nil
}

func (l *llmCallAuditor) afterModel(ctx agent.Context, response *model.LLMResponse, responseErr error) (*model.LLMResponse, error) {
	scope, ok := l.scope(ctx.SessionID())
	if !ok {
		return response, responseErr
	}
	modelName := scope.model
	if response != nil && response.ModelVersion != "" {
		modelName = response.ModelVersion
	}
	callbackKey, err := llmCallbackKey(ctx)
	if err != nil {
		return response, err
	}
	scope.mu.Lock()
	runtime, exists := scope.activeCalls[callbackKey]
	scope.mu.Unlock()
	if !exists {
		return response, responseErr
	}

	finishedAt := l.now()
	call := &domain.LLMCall{
		ID:                runtime.id,
		RunID:             scope.runID,
		ThreadID:          scope.threadID,
		UserID:            scope.userID,
		AgentName:         ctx.AgentName(),
		Model:             modelName,
		Provider:          l.provider,
		Status:            domain.LLMCallCompleted,
		PromptVersion:     scope.instructionDigest,
		DefinitionName:    scope.definitionName,
		DefinitionVersion: scope.definitionVersion,
		DefinitionDigest:  scope.definitionDigest,
		StartedAt:         runtime.startedAt,
		FinishedAt:        finishedAt,
	}
	call.DurationMS = finishedAt.Sub(call.StartedAt).Milliseconds()
	if responseErr != nil {
		call.Fail(responseErr, finishedAt)
	} else if response != nil && response.UsageMetadata != nil {
		call.Complete(llmUsageFromResponse(response.UsageMetadata), finishedAt)
	} else {
		call.Complete(domain.LLMTokenUsage{}, finishedAt)
	}
	call.Normalize()
	scope.mu.Lock()
	scope.llmCalls++
	scope.promptTokens += int(call.PromptTokens)
	scope.completionTokens += int(call.CompletionTokens)
	scope.thoughtTokens += int(call.ThoughtTokens)
	scope.mu.Unlock()
	l.applyAgent(call, call.AgentName)
	if err := call.Validate(); err != nil {
		return response, err
	}
	terminalEvent := audit.EventLLMCallCompleted
	terminalStatus := audit.StatusSucceeded
	if call.Status == domain.LLMCallFailed {
		terminalEvent = audit.EventLLMCallFailed
		terminalStatus = audit.StatusFailed
	}
	if auditErr := l.recordCall(context.WithoutCancel(ctx), scope, call, terminalEvent, terminalStatus); auditErr != nil {
		return response, auditErr
	}
	scope.mu.Lock()
	// Retain the active call if Record fails so run reconciliation can emit a
	// terminal audit fact rather than leaving started evidence permanently open.
	delete(scope.activeCalls, callbackKey)
	scope.mu.Unlock()
	if l.metrics != nil {
		l.metrics.ObserveLLMCall(call.Model, string(call.Status), domain.LLMTokenUsage{
			PromptTokens:     call.PromptTokens,
			CompletionTokens: call.CompletionTokens,
			ThoughtTokens:    call.ThoughtTokens,
			TotalTokens:      call.TotalTokens,
		})
	}
	return response, responseErr
}

func (l *llmCallAuditor) onModelError(ctx agent.Context, request *model.LLMRequest, requestErr error) (*model.LLMResponse, error) {
	scope, ok := l.scope(ctx.SessionID())
	if !ok || requestErr == nil {
		return nil, requestErr
	}
	modelName := scope.model
	if request != nil && request.Model != "" {
		modelName = request.Model
	}
	callbackKey, err := llmCallbackKey(ctx)
	if err != nil {
		return nil, err
	}
	scope.mu.Lock()
	runtime, exists := scope.activeCalls[callbackKey]
	if !exists {
		scope.nextCall++
		callSeq := scope.nextCall
		runtime = llmCallRuntime{
			id:        fmt.Sprintf("%s/%s/%d", ctx.InvocationID(), modelName, callSeq),
			model:     modelName,
			agentName: ctx.AgentName(),
			startedAt: l.now(),
		}
	}
	scope.mu.Unlock()
	if !exists {
		// ADK invoked the model-error callback without a matching BeforeModel
		// callback, so no started fact exists yet. Audit evidence requires
		// started and terminal evidence for every model call; synthesize the
		// missing started fact before recording the failure.
		if err := l.recordCallStarted(ctx, scope, runtime); err != nil {
			return nil, err
		}
	}
	finishedAt := l.now()
	call := &domain.LLMCall{
		ID:                runtime.id,
		RunID:             scope.runID,
		ThreadID:          scope.threadID,
		UserID:            scope.userID,
		AgentName:         ctx.AgentName(),
		Model:             modelName,
		Provider:          l.provider,
		Status:            domain.LLMCallFailed,
		PromptVersion:     scope.instructionDigest,
		DefinitionName:    scope.definitionName,
		DefinitionVersion: scope.definitionVersion,
		DefinitionDigest:  scope.definitionDigest,
		StartedAt:         runtime.startedAt,
		FinishedAt:        finishedAt,
	}
	call.DurationMS = finishedAt.Sub(call.StartedAt).Milliseconds()
	call.Fail(requestErr, finishedAt)
	call.Normalize()
	l.applyAgent(call, call.AgentName)
	if err := call.Validate(); err != nil {
		return nil, err
	}
	if auditErr := l.recordCall(context.WithoutCancel(ctx), scope, call, audit.EventLLMCallFailed, audit.StatusFailed); auditErr != nil {
		return nil, auditErr
	}
	if exists {
		scope.mu.Lock()
		delete(scope.activeCalls, callbackKey)
		scope.mu.Unlock()
	}
	if l.metrics != nil {
		l.metrics.ObserveLLMCall(call.Model, string(call.Status), domain.LLMTokenUsage{
			PromptTokens:     call.PromptTokens,
			CompletionTokens: call.CompletionTokens,
			ThoughtTokens:    call.ThoughtTokens,
			TotalTokens:      call.TotalTokens,
		})
	}
	return nil, requestErr
}

func (l *llmCallAuditor) failActive(scope *runScope, err error, finishedAt time.Time) error {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	var firstErr error
	for callbackKey, runtime := range scope.activeCalls {
		call := &domain.LLMCall{
			ID: runtime.id, RunID: scope.runID, ThreadID: scope.threadID,
			UserID: scope.userID, AgentName: runtime.agentName, Model: runtime.model,
			Provider: l.provider, Status: domain.LLMCallFailed,
			PromptVersion: runtime.instructionDigest,
			StartedAt:     runtime.startedAt, FinishedAt: finishedAt,
		}
		call.DurationMS = finishedAt.Sub(call.StartedAt).Milliseconds()
		call.Fail(err, finishedAt)
		l.applyAgent(call, call.AgentName)
		recordErr := l.recordCall(context.Background(), scope, call, audit.EventLLMCallFailed, audit.StatusFailed)
		if recordErr == nil {
			delete(scope.activeCalls, callbackKey)
		} else if firstErr == nil {
			firstErr = recordErr
		}
	}
	return firstErr
}

func (l *llmCallAuditor) reconcile(scope *runScope, cause error) error {
	if scope == nil {
		return nil
	}
	return l.failActive(scope, cause, l.now())
}

func (l *llmCallAuditor) applyAgent(call *domain.LLMCall, name string) {
	definition, ok := l.agents[name]
	if !ok {
		return
	}
	call.AgentName = definition.Name
	call.AgentVersion = definition.Version
	call.AgentDefinitionDigest = definition.Digest
	call.PromptVersion = definition.InstructionDigest
}

// recordCallStarted appends the started fact for a model call that ADK reported
// without a matching BeforeModel callback, preserving the audit rule that
// every model call has started and terminal evidence.
func (l *llmCallAuditor) recordCallStarted(ctx agent.Context, scope *runScope, runtime llmCallRuntime) error {
	call := &domain.LLMCall{
		ID:                runtime.id,
		RunID:             scope.runID,
		ThreadID:          scope.threadID,
		UserID:            scope.userID,
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
	call.Normalize()
	l.applyAgent(call, call.AgentName)
	if err := call.Validate(); err != nil {
		return err
	}
	return l.recordCall(context.WithoutCancel(ctx), scope, call, audit.EventLLMCallStarted, audit.StatusRunning)
}

func (l *llmCallAuditor) recordCall(ctx context.Context, scope *runScope, call *domain.LLMCall, eventType audit.EventType, status audit.Status) error {
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
	if event.TraceID == "" && scope.traceID != "" {
		// Reconciliation paths may lack a live span context (for example
		// failActive). Fall back to the run-scoped trace so reconciled
		// terminal events stay correlatable (DOMAIN invariant 9).
		event.TraceID = scope.traceID
		event.SpanID = scope.spanID
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
	if err := event.Validate(); err != nil {
		return err
	}
	return l.recorder.Record(ctx, &event)
}

func llmUsageFromResponse(usage *genai.GenerateContentResponseUsageMetadata) domain.LLMTokenUsage {
	if usage == nil {
		return domain.LLMTokenUsage{}
	}
	return domain.LLMTokenUsage{
		PromptTokens:     int64(usage.PromptTokenCount),
		CompletionTokens: int64(usage.CandidatesTokenCount),
		ThoughtTokens:    int64(usage.ThoughtsTokenCount),
		TotalTokens:      int64(usage.TotalTokenCount),
	}
}

func llmCallbackKey(ctx agent.Context) (string, error) {
	invocationID := strings.TrimSpace(ctx.InvocationID())
	if invocationID == "" {
		return "", domain.NewError(domain.CodeRuntimeFailed, "ADK invocation id is required for LLM audit correlation", nil)
	}
	return invocationID, nil
}
