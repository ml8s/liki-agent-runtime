package agent

import (
	"context"
	"errors"
	"strings"

	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
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
	ctx = WithClientTools(ctx, request.ClientTools)
	if err := r.ensureDurableRunID(ctx, request.RunID); err != nil {
		r.observeRunIDRejection(err)
		return execution.RunResult{}, err
	}
	if !r.runs.begin(request.RunID) {
		r.observeRunRejection("run_id_conflict")
		return execution.RunResult{}, domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
	}
	sessionID := runSessionID(request.RunID)
	// Runtime.Run owns a run-scoped session, so the ADK session and attempt IDs
	// happen to coincide. Keeping the distinction explicit makes the externally
	// driven A2A path (ContextID != attempt ID) unambiguous.
	handle := AuditAttemptHandle{ContextID: sessionID, AttemptID: domain.AttemptID(request.RunID)}
	ctx = WithAuditAttempt(ctx, string(handle.AttemptID))
	releaseRun, err := r.acquireRun(ctx)
	if err != nil {
		r.runs.abort(request.RunID)
		return execution.RunResult{}, err
	}
	// The concurrency slot is always released when this request exits. The
	// lifecycle registry is released only after terminal evidence succeeds; a
	// failed terminal append remains reconcilable and its RunID stays invalid
	// for reuse rather than silently becoming executable again.
	defer releaseRun()

	ctx, runSpan := r.tracer.Start(
		ctx,
		"agent.run "+r.entrypoint.Name,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("agent.runtime.id", request.RunID),
			attribute.String("agent.runtime.root_id", request.RunID),
			attribute.String("agent.thread.id", request.ThreadID),
			attribute.String("gen_ai.agent.name", r.entrypoint.Name),
			attribute.String("agent.agent.version", r.entrypoint.Version),
			attribute.String("agent.definition.name", r.definition.Metadata.Name),
			attribute.String("agent.definition.version", r.definition.Metadata.Version),
			attribute.String("agent.definition.digest", r.definition.Digest),
		),
	)
	defer runSpan.End()
	startedAt := r.config.Now()
	scope := r.newRunExecution(
		execution.RunIdentity{
			RunID:    domain.ID(request.RunID),
			ThreadID: domain.ID(request.ThreadID),
			UserID:   request.UserID,
		},
		execution.RunProvenance{
			Protocol: request.Protocol,
			TraceID:  traceIDFromContext(ctx),
			SpanID:   spanIDFromContext(ctx),
		},
		"",
		startedAt,
		nil,
	)
	// Aggregate usage is filled in on every exit path (including failure
	// and cancellation).
	defer func() { result.Usage = scope.usage() }()
	if startErr := r.startRunExecution(ctx, handle, scope); startErr != nil {
		_ = r.ephemeralSessions.Delete(context.WithoutCancel(ctx), &session.DeleteRequest{
			AppName:   r.config.AppName,
			UserID:    request.UserID,
			SessionID: sessionID,
		})
		r.runs.abort(request.RunID)
		runErr = startErr
		return execution.RunResult{}, runErr
	}
	defer func() {
		auditCtx := context.WithoutCancel(ctx)
		// Terminal evidence transaction (single implementation in
		// run_evidence.go): the terminal event precedes any process-state
		// release.
		outcome := auditOutcomeCompleted
		if runErr != nil {
			outcome = auditOutcomeFailed
		}
		claimed, wasPending := r.runs.claimClose(request.RunID)
		if !claimed {
			runErr = domain.NewError(domain.CodeRuntimeBusy, "run terminal closure is already in progress", nil)
			return
		}
		terminalErr := r.closeRunEvidence(auditCtx, scope, runErr, outcome)
		if terminalErr != nil {
			if runErr == nil {
				runErr = terminalErr
			}
			if !wasPending && r.runs.failClose(request.RunID) {
				r.observePendingTerminalRuns(1)
			}
			// The request may end, but evidence and lifecycle identity stay
			// active. A bounded reconciler owns terminal retries; startup
			// recovery is the last resort if the process exits first.
			go r.reconcileRunTerminalEvidence(handle, scope, runErr, outcome)
			runtimeErr := runtimeError(runErr)
			runSpan.RecordError(runtimeErr)
			runSpan.SetStatus(codes.Error, runtimeErr.Error())
			return
		}

		// Terminal evidence is durable. Process state can now be released.
		// Cleanup failures are operational noise and do not change the durably
		// recorded execution outcome.
		r.callAuditor.forget(handle)
		if deleteErr := r.ephemeralSessions.Delete(auditCtx, &session.DeleteRequest{
			AppName:   r.config.AppName,
			UserID:    request.UserID,
			SessionID: sessionID,
		}); deleteErr != nil {
			r.config.Logger.WarnContext(ctx, "agent_run_session_cleanup_failed",
				"run_id", request.RunID,
				"error", runtimeError(deleteErr).Error(),
			)
		}
		r.runs.finish(request.RunID)
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

	state, streamErr := r.seedAndStreamEvents(ctx, request, sessionID, scope, observe, runSpan)
	if streamErr != nil {
		runErr = streamErr
		return execution.RunResult{}, runErr
	}
	result, finalizeErr := r.finalizeRunResult(ctx, request, sessionID, state, runSpan)
	if finalizeErr != nil {
		runErr = finalizeErr
		return execution.RunResult{}, runErr
	}
	return result, nil
}

// seedAndStreamEvents seeds conversation history into the run-scoped session,
// then iterates ADK events and advances the run state. Each error is already
// recorded on the run span and logged before being returned.
func (r *Runtime) seedAndStreamEvents(
	ctx context.Context,
	request execution.RunRequest,
	sessionID string,
	scope *runExecution,
	observe func(*session.Event) error,
	span trace.Span,
) (RunState, error) {
	if err := r.seedConversationHistory(ctx, r.ephemeralSessions, request, sessionID); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, runtimeError(err).Error())
		return RunState{}, err
	}

	events := r.ephemeralRunner.Run(
		ctx,
		request.UserID,
		sessionID,
		BuildUserContent(request),
		agent.RunConfig{StreamingMode: agent.StreamingModeSSE},
	)

	state := NewRunState(deploymentTextPolicy{definition: r.definition})
	projector := NewEventProjector(r.projectorIndex)
	projection := NewEventProjectionState()

	for event, err := range events {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, runtimeError(err).Error())
			r.config.Logger.WarnContext(ctx, "agent_run_failed",
				append(traceLogFields(ctx),
					"run_id", request.RunID, "error", runtimeError(err).Error(),
				)...)
			return state, runtimeError(err)
		}
		state = state.Consume(event)
		var structuredErr error
		state, structuredErr = r.consumeStructuredEvent(state, event)
		if structuredErr != nil {
			span.RecordError(structuredErr)
			span.SetStatus(codes.Error, runtimeError(structuredErr).Error())
			return state, structuredErr
		}
		// Task-mode sub-agents stop their event iterator on finish_task, so
		// their AfterAgent callback never runs; complete the delegation from
		// the synthesized task-tool function response instead.
		if delegationErr := r.delegationAuditor.CompleteByToolResponse(ctx, scope, event); delegationErr != nil {
			span.RecordError(delegationErr)
			span.SetStatus(codes.Error, runtimeError(delegationErr).Error())
			return state, runtimeError(delegationErr)
		}
		if observe != nil {
			var visible *session.Event
			var visibleOK bool
			visible, projection, visibleOK = projector.Project(projection, event)
			if visibleOK {
				if err := observe(visible); err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, runtimeError(err).Error())
					return state, runtimeError(err)
				}
			}
		}
	}
	return state, nil
}

// finalizeRunResult applies structured-output fallback, rejects an empty
// answer, and assembles the final result. On success it marks the span and
// logs the completion event.
func (r *Runtime) finalizeRunResult(
	ctx context.Context,
	request execution.RunRequest,
	sessionID string,
	state RunState,
	span trace.Span,
) (execution.RunResult, error) {
	if r.entrypoint.Output.Structured() && state.Output.JSON == nil {
		var loadErr error
		state, loadErr = r.loadStructuredAnalysis(ctx, request, sessionID, state)
		if loadErr != nil {
			span.RecordError(loadErr)
			span.SetStatus(codes.Error, runtimeError(loadErr).Error())
			return execution.RunResult{}, loadErr
		}
	}

	if strings.TrimSpace(state.Output.Text) == "" {
		r.config.Logger.Warn("agent_run_empty_response", "run_id", request.RunID)
		return execution.RunResult{}, domain.NewError(domain.CodeRuntimeEmptyResponse, "ADK runtime returned no answer text", nil)
	}

	span.SetStatus(codes.Ok, "")
	r.config.Logger.InfoContext(ctx, "agent_run_completed",
		append(traceLogFields(ctx),
			"run_id", request.RunID,
			"agent", r.entrypoint.Name,
			"tools_used", state.Tools(),
			"model", r.entrypoint.EffectiveModel(r.config.Model),
		)...)

	return execution.RunResult{
		Definition: r.definition.DeploymentRef(),
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
	state RunState,
	event *session.Event,
) (RunState, error) {
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
	if r.config.RequireDurableRunIDs {
		checker, ok := r.config.AuditRecorder.(audit.RunExistenceChecker)
		if !ok {
			return domain.NewError(domain.CodeRuntimeInitFailed, "durable run-id existence checking is unavailable", nil)
		}
		exists, err := checker.RunExists(ctx, domain.ID(runID))
		if err != nil {
			return err
		}
		if exists {
			r.observeRunReservation("conflict")
			return domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
		}
		reservation, ok := r.config.AuditRecorder.(audit.RunReservationStore)
		if !ok {
			return domain.NewError(domain.CodeRuntimeInitFailed, "durable run-id reservation is unavailable", nil)
		}
		reservationErr := reservation.ReserveRun(ctx, domain.ID(runID))
		switch reservationErr {
		case nil:
			r.observeRunReservation("reserved")
		default:
			var domainErr *domain.Error
			if errors.As(reservationErr, &domainErr) && domainErr.Code == domain.CodeRunIDConflict {
				r.observeRunReservation("conflict")
			} else {
				r.observeRunReservation("failed")
			}
		}
		return reservationErr
	}
	checker, ok := r.config.AuditRecorder.(audit.RunExistenceChecker)
	if !ok {
		return nil
	}
	exists, err := checker.RunExists(ctx, domain.ID(runID))
	if err != nil {
		return err
	}
	if exists {
		r.observeRunReservation("conflict")
		return domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
	}
	return nil
}

func (r *Runtime) observeRunReservation(result string) {
	if r.config.Metrics != nil {
		r.config.Metrics.ObserveRunReservation(result)
	}
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
	state RunState,
) (RunState, error) {
	response, err := r.ephemeralSessions.Get(ctx, &session.GetRequest{
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
func (r *Runtime) ResolvedEntrypoint() ResolvedEntrypointView {
	return newResolvedEntrypointView(r.entrypoint)
}

// AgentDeployment exposes a read-only view of non-secret deployment metadata for standard protocol
// discovery. Instructions and schemas remain internal to the runtime.
func (r *Runtime) AgentDeployment() agentdeployment.DeploymentView {
	return r.definition.View()
}

func (r *Runtime) observeRunIDRejection(err error) {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) && domainErr.Code == domain.CodeRunIDConflict {
		r.observeRunRejection("run_id_conflict")
	}
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
