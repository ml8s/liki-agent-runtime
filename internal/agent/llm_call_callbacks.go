package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

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
	call := domain.LLMCall{
		ID:                callID,
		RunID:             scope.identity.RunID,
		ThreadID:          scope.identity.ThreadID,
		UserID:            scope.identity.UserID,
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
	l.applyAgent(&call, call.AgentName)
	if err := call.Validate(); err != nil {
		return nil, err
	}
	if err := l.recordCall(ctx, scope, &call, audit.EventLLMCallStarted, audit.StatusRunning); err != nil {
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
	call := domain.LLMCall{
		ID:                runtime.id,
		RunID:             scope.identity.RunID,
		ThreadID:          scope.identity.ThreadID,
		UserID:            scope.identity.UserID,
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
	if responseErr != nil {
		call = domain.FailLLMCall(call, responseErr, finishedAt)
	} else if response != nil && response.UsageMetadata != nil {
		call = domain.CompleteLLMCall(call, llmUsageFromResponse(response.UsageMetadata), finishedAt)
	} else {
		call = domain.CompleteLLMCall(call, domain.LLMTokenUsage{}, finishedAt)
	}
	call = domain.NormalizeLLMCall(call)
	scope.mu.Lock()
	scope.llmCalls++
	scope.promptTokens += int(call.PromptTokens)
	scope.completionTokens += int(call.CompletionTokens)
	scope.thoughtTokens += int(call.ThoughtTokens)
	scope.mu.Unlock()
	l.applyAgent(&call, call.AgentName)
	if err := call.Validate(); err != nil {
		return response, err
	}
	terminalEvent := audit.EventLLMCallCompleted
	terminalStatus := audit.StatusSucceeded
	if call.Status == domain.LLMCallFailed {
		terminalEvent = audit.EventLLMCallFailed
		terminalStatus = audit.StatusFailed
	}
	if auditErr := l.recordCall(context.WithoutCancel(ctx), scope, &call, terminalEvent, terminalStatus); auditErr != nil {
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
		l.metrics.ObserveLLMCallLatency(
			call.Model,
			string(call.Status),
			time.Duration(call.DurationMS)*time.Millisecond,
		)
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
	call := domain.LLMCall{
		ID:                runtime.id,
		RunID:             scope.identity.RunID,
		ThreadID:          scope.identity.ThreadID,
		UserID:            scope.identity.UserID,
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
	call = domain.FailLLMCall(call, requestErr, finishedAt)
	call = domain.NormalizeLLMCall(call)
	l.applyAgent(&call, call.AgentName)
	if err := call.Validate(); err != nil {
		return nil, err
	}
	if auditErr := l.recordCall(context.WithoutCancel(ctx), scope, &call, audit.EventLLMCallFailed, audit.StatusFailed); auditErr != nil {
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
		l.metrics.ObserveLLMCallLatency(
			call.Model,
			string(call.Status),
			time.Duration(call.DurationMS)*time.Millisecond,
		)
	}
	return nil, requestErr
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
