package agent

import agentdeployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"context"
	"errors"
	"strings"

	"github.com/ml8s/liki-agent-runtime/internal/execution"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// Run executes the shared ADK runtime and exposes native ADK events to a
// protocol adapter. It is not a public API and creates no second business path.
func (r *Runtime) Run(
	ctx context.Context,
	request execution.RunRequest,
	observe func(*session.Event) error,
) (result execution.RunResult, runErr error) {
	request = normalizeRunRequest(request)
	if err := validateRunRequest(request); err != nil {
		return execution.RunResult{}, err
	}
	if err := r.ensureDurableRunID(ctx, request.RunID); err != nil {
		return execution.RunResult{}, err
	}
	if !r.runs.begin(request.RunID) {
		return execution.RunResult{}, domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
	}
	releaseRun, err := r.acquireRun(ctx)
	if err != nil {
		r.runs.finish(request.RunID)
		return execution.RunResult{}, err
	}
	defer func() {
		releaseRun()
		r.runs.finish(request.RunID)
	}()

	ctx, runSpan := r.tracer.Start(
		ctx,
		"agent.run "+r.entrypoint.Name,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("liki.run.id", request.RunID),
			attribute.String("liki.run.root_id", request.RunID),
			attribute.String("liki.thread.id", request.ThreadID),
			attribute.String("gen_ai.agent.name", r.entrypoint.Name),
			attribute.String("liki.agent.version", r.entrypoint.Version),
			attribute.String("liki.definition.name", r.definition.Metadata.Name),
			attribute.String("liki.definition.version", r.definition.Metadata.Version),
			attribute.String("liki.definition.digest", r.definition.Digest),
		),
	)
	defer runSpan.End()
	startedAt := r.config.Now()
	state := execution.RunState{
		CapturePlain: !r.entrypoint.Output.Structured(),
	}
	projector := execution.NewEventProjector(r.projectorIndex)
	projection := execution.NewEventProjectionState()
	sessionID := runSessionID(request.RunID)
	scope := &runExecution{
		identity: execution.RunIdentity{
			RunID:    domain.ID(request.RunID),
			ThreadID: domain.ID(request.ThreadID),
			UserID:   request.UserID,
		},
		provenance: execution.RunProvenance{
			Protocol: request.Protocol,
			TraceID:  traceIDFromContext(ctx),
			SpanID:   spanIDFromContext(ctx),
		},
		model:             r.entrypoint.EffectiveModel(r.config.Model),
		instructionDigest: r.entrypoint.InstructionDigest,
		definitionName:    r.definition.Metadata.Name,
		definitionVersion: r.definition.Metadata.Version,
		definitionDigest:  r.definition.Digest,
		startedAt:         startedAt,
		activeCalls:       make(map[string]llmCallRuntime),
	}
	// 聚合用量在所有退出路径回填（含失败与取消）。
	defer func() { result.Usage = scope.usage() }()
	if !r.callAuditor.begin(sessionID, scope) {
		runErr = domain.NewError(domain.CodeAuditSessionActive, "an audit run is already active for this session", domain.ErrInvalidInput)
		return execution.RunResult{}, runErr
	}
	if auditErr := r.recordRunAudit(ctx, scope, audit.EventRunStarted, audit.StatusRunning, nil); auditErr != nil {
		runErr = auditErr
		auditScope, _ := r.callAuditor.scope(sessionID)
		_, _ = r.callAuditor.end(sessionID, runErr)
		_ = r.delegationAuditor.FailPending(context.WithoutCancel(ctx), auditScope)
		_ = r.toolAuditor.FailPending(context.WithoutCancel(ctx), auditScope)
		_ = r.sessions.Delete(context.WithoutCancel(ctx), &session.DeleteRequest{
			AppName:   r.config.AppName,
			UserID:    request.UserID,
			SessionID: sessionID,
		})
		return execution.RunResult{}, runErr
	}
	defer func() {
		auditCtx := context.WithoutCancel(ctx)
		// 终态证据事务（唯一实现见 run_evidence.go）：终态事件先于任何
		// 进程状态释放。
		if err := r.closeRunEvidence(auditCtx, scope, runErr); err != nil && runErr == nil {
			runErr = err
		}
		// 终态事件已尝试落盘；此后释放进程状态。清理失败属运维噪音，
		// 不改变已持久化的执行结果。
		r.callAuditor.forget(sessionID)
		if deleteErr := r.sessions.Delete(auditCtx, &session.DeleteRequest{
			AppName:   r.config.AppName,
			UserID:    request.UserID,
			SessionID: sessionID,
		}); deleteErr != nil {
			r.config.Logger.WarnContext(ctx, "agent_run_session_cleanup_failed",
				"run_id", request.RunID,
				"error", runtimeError(deleteErr).Error(),
			)
		}
		if runErr != nil {
			runtimeErr := runtimeError(runErr)
			runSpan.RecordError(runtimeErr)
			runSpan.SetStatus(codes.Error, runtimeErr.Error())
		}
	}()
	r.config.Logger.InfoContext(ctx, "agent_run_started",
		append(traceLogFields(ctx),
			"run_id", request.RunID,
			"thread_id", request.ThreadID,
			"user_id", request.UserID,
			"model", r.entrypoint.EffectiveModel(r.config.Model),
		)...)

	if err := r.seedConversationHistory(ctx, request, sessionID); err != nil {
		runSpan.RecordError(err)
		runSpan.SetStatus(codes.Error, runtimeError(err).Error())
		runErr = err
		return execution.RunResult{}, runErr
	}

	events := r.runner.Run(
		ctx,
		request.UserID,
		sessionID,
		execution.BuildUserContent(request),
		agent.RunConfig{StreamingMode: agent.StreamingModeSSE},
	)
	for event, err := range events {
		if err != nil {
			runSpan.RecordError(err)
			runSpan.SetStatus(codes.Error, runtimeError(err).Error())
			r.config.Logger.WarnContext(ctx, "agent_run_failed",
				append(traceLogFields(ctx),
					"run_id", request.RunID, "error", runtimeError(err).Error(),
				)...)
			runErr = runtimeError(err)
			return execution.RunResult{}, runErr
		}
		state = state.Consume(event)
		var structuredErr error
		state, structuredErr = r.consumeStructuredEvent(state, event)
		if structuredErr != nil {
			runSpan.RecordError(structuredErr)
			runSpan.SetStatus(codes.Error, runtimeError(structuredErr).Error())
			runErr = structuredErr
			return execution.RunResult{}, runErr
		}
		// Task-mode sub-agents stop their event iterator on finish_task, so
		// their AfterAgent callback never runs; complete the delegation from
		// the synthesized task-tool function response instead.
		if delegationErr := r.delegationAuditor.CompleteByToolResponse(ctx, scope, event); delegationErr != nil {
			runSpan.RecordError(delegationErr)
			runSpan.SetStatus(codes.Error, runtimeError(delegationErr).Error())
			runErr = runtimeError(delegationErr)
			return execution.RunResult{}, runErr
		}
		if observe != nil {
			var visible *session.Event
			var visibleOK bool
			visible, projection, visibleOK = projector.Project(projection, event)
			if visibleOK {
				if err := observe(visible); err != nil {
					runSpan.RecordError(err)
					runSpan.SetStatus(codes.Error, runtimeError(err).Error())
					runErr = runtimeError(err)
					return execution.RunResult{}, runErr
				}
			}
		}
	}
	if r.entrypoint.Output.Structured() && state.Output.JSON == nil {
		var loadErr error
		state, loadErr = r.loadStructuredAnalysis(ctx, request, sessionID, state)
		if loadErr != nil {
			runSpan.RecordError(loadErr)
			runSpan.SetStatus(codes.Error, runtimeError(loadErr).Error())
			runErr = loadErr
			return execution.RunResult{}, runErr
		}
	}

	if strings.TrimSpace(state.Output.Text) == "" {
		r.config.Logger.Warn("agent_run_empty_response", "run_id", request.RunID)
		runErr = domain.NewError(domain.CodeRuntimeEmptyResponse, "ADK runtime returned no answer text", nil)
		return execution.RunResult{}, runErr
	}
	runSpan.SetStatus(codes.Ok, "")
	r.config.Logger.InfoContext(ctx, "agent_run_completed",
		append(traceLogFields(ctx),
			"run_id", request.RunID,
			"agent", r.entrypoint.Name,
			"tools_used", state.Tools(),
			"model", r.entrypoint.EffectiveModel(r.config.Model),
		)...)

	return execution.RunResult{
		Definition: r.definition.DefinitionRef(),
		Output:     state.Output.JSON,
		Text:       state.Output.Text,
	}, nil
}

// consumeStructuredEvent reads the entrypoint's structured output directly
// from the final event's StateDelta. ADK persists the same value into session
// state, so consuming the event avoids a full session read-back after the
// stream; loadStructuredAnalysis remains the defensive fallback for streams
// that yielded no state delta. Repeated final events follow the session's
// last-write-wins semantics because each delta overwrites the prior result.
func (r *Runtime) consumeStructuredEvent(
	state execution.RunState,
	event *session.Event,
) (execution.RunState, error) {
	if !r.entrypoint.Output.Structured() ||
		event == nil ||
		!event.IsFinalResponse() ||
		event.Author != r.entrypoint.Name {
		return state, nil
	}
	value, ok := event.Actions.StateDelta[execution.StructuredOutputStateKey(r.entrypoint.Name)]
	if !ok {
		return state, nil
	}
	return state.ConsumeStructuredOutput(value, r.entrypoint)
}

func (r *Runtime) ensureDurableRunID(ctx context.Context, runID string) error {
	checker, ok := r.config.AuditRecorder.(audit.RunExistenceChecker)
	if !ok {
		return nil
	}
	exists, err := checker.RunExists(ctx, domain.ID(runID))
	if err != nil {
		return err
	}
	if exists {
		return domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
	}
	return nil
}

func (r *Runtime) recordRunAudit(
	ctx context.Context,
	scope *runExecution,
	eventType audit.EventType,
	status audit.Status,
	cause error,
) error {
	event := r.runEvent(ctx, scope, eventType, status, cause)
	event, err := audit.ValidateEvent(event)
	if err != nil {
		return err
	}
	return r.recordAuditWithRetry(ctx, &event)
}

func (r *Runtime) recordAuditWithRetry(ctx context.Context, event *audit.Event) error {
	err := r.config.AuditRecorder.Record(ctx, event)
	if err == nil || !retryableAuditAppend(err) {
		return err
	}
	// SQLite busy and transient exporter failures are the common case. A
	// second append attempt cannot overwrite the append-only event ID.
	return r.config.AuditRecorder.Record(ctx, event)
}

// retryableAuditAppend reports whether a failed append is worth an immediate
// second attempt. Event-contract violations wrap domain.ErrInvalidInput and
// conflicts are deterministic: retrying either cannot change the outcome.
// Infrastructure failures (busy, timeout, exporter) remain retryable.
func retryableAuditAppend(err error) bool {
	if errors.Is(err, domain.ErrInvalidInput) {
		return false
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) && domainErr.Code == audit.CodeAuditEventConflict {
		return false
	}
	return true
}

// loadStructuredAnalysis is the defensive fallback that consumes the parsed
// model output from ADK session state when the event stream carried no state
// delta. It reads the whole run-scoped session, so the event path above is
// preferred.
func (r *Runtime) loadStructuredAnalysis(
	ctx context.Context,
	request execution.RunRequest,
	sessionID string,
	state execution.RunState,
) (execution.RunState, error) {
	response, err := r.sessions.Get(ctx, &session.GetRequest{
		AppName:   r.config.AppName,
		UserID:    request.UserID,
		SessionID: sessionID,
	})
	if err != nil {
		r.config.Logger.Warn("agent_session_state_unavailable",
			"run_id", request.RunID, "error", runtimeError(err).Error())
		return state, domain.NewError(domain.CodeRuntimeSessionUnavailable, "read ADK session state", err)
	}
	if response == nil || response.Session == nil {
		return state, nil
	}
	value, err := response.Session.State().Get(execution.StructuredOutputStateKey(r.entrypoint.Name))
	if err != nil {
		return state, nil
	}
	return state.ConsumeStructuredOutput(value, r.entrypoint)
}

// Entrypoint exposes the selected AgentDefinition to protocol adapters.
func (r *Runtime) Entrypoint() *agentdeployment.AgentDefinition {
	clone := cloneAgentDefinition(&r.entrypoint.AgentDefinition)
	return &clone
}

// ResolvedEntrypoint exposes the loaded runtime-neutral state required by
// structured-output protocol mappings. Instructions remain internal to callers
// that only render discovery metadata.
func (r *Runtime) ResolvedEntrypoint() *agentdeployment.ResolvedAgentDefinition {
	return r.entrypoint
}

// AgentDeployment exposes non-secret deployment metadata for standard protocol
// discovery. Instructions and schemas remain internal to the runtime.
func (r *Runtime) AgentDeployment() *agentdeployment.AgentDeployment {
	deployment := *r.definition
	deployment.Spec.MCPServers = append([]agentdeployment.MCPServerDefinition(nil), r.definition.Spec.MCPServers...)
	deployment.Spec.Agents = make([]agentdeployment.ResolvedAgentDefinition, len(r.definition.Spec.Agents))
	for index := range r.definition.Spec.Agents {
		deployment.Spec.Agents[index] = cloneResolvedAgentDefinition(&r.definition.Spec.Agents[index])
	}
	return &deployment
}

func cloneResolvedAgentDefinition(definition *agentdeployment.ResolvedAgentDefinition) agentdeployment.ResolvedAgentDefinition {
	clone := *definition
	clone.AgentDefinition = cloneAgentDefinition(&definition.AgentDefinition)
	return clone
}

func cloneAgentDefinition(definition *agentdeployment.AgentDefinition) agentdeployment.AgentDefinition {
	clone := *definition
	clone.SubAgents = append([]agentdeployment.AgentReference(nil), definition.SubAgents...)
	clone.Tools.Allow = make(map[string][]string, len(definition.Tools.Allow))
	for server, tools := range definition.Tools.Allow {
		clone.Tools.Allow[server] = append([]string(nil), tools...)
	}
	return clone
}

func traceLogFields(ctx context.Context) []any {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return nil
	}
	return []any{"trace_id", spanContext.TraceID().String(), "span_id", spanContext.SpanID().String()}
}

func traceIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func spanIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}
