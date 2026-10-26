package agent

import agentdeployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"go.opentelemetry.io/otel/trace"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// DelegationExecutionRecord is runtime evidence for one standard ADK
// sub-agent invocation. It does not encode domain workflow semantics.
type DelegationExecutionRecord struct {
	CallerAgent           string
	TargetAgent           string
	AgentDefinitionDigest string
	RootRunID             domain.ID
	RunID                 domain.ID
	ThreadID              domain.ID
	UserID                string
	AgentPath             string
	Depth                 int
	Status                audit.Status
	StartedAt             time.Time
	FinishedAt            time.Time
	DurationMS            int64
	ErrorCode             string
	ErrorMessage          string
}

// DelegationAuditor instruments ADK's standard BeforeAgent and
// AfterAgent callbacks. The entrypoint is excluded because Runtime.Run already
// records its run lifecycle.
type DelegationAuditor struct {
	recorder   audit.Recorder
	metrics    Metrics
	now        func() time.Time
	entrypoint string
	agents     map[string]*agentdeployment.ResolvedAgentDefinition
	scope      func(sessionID string) (*runExecution, bool)

	mu      sync.Mutex
	pending map[string]DelegationExecutionRecord
}

func newDelegationAuditor(
	recorder audit.Recorder,
	metrics Metrics,
	now func() time.Time,
	deployment *agentdeployment.AgentDeployment, entrypoint string,
	scope func(sessionID string) (*runExecution, bool),
) *DelegationAuditor {
	agents := make(map[string]*agentdeployment.ResolvedAgentDefinition, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		definition := &deployment.Spec.Agents[index]
		agents[definition.Name] = definition
	}
	return &DelegationAuditor{
		recorder:   recorder,
		metrics:    metrics,
		now:        now,
		entrypoint: entrypoint,
		agents:     agents,
		scope:      scope,
		pending:    make(map[string]DelegationExecutionRecord),
	}
}

// BeforeAgent records a delegated Agent invocation. The deployment entrypoint
// is skipped because Runtime.Run records the top-level run lifecycle.
func (a *DelegationAuditor) BeforeAgent(ctx adkagent.Context) (*genai.Content, error) {
	if ctx == nil {
		return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, "agent callback context is required", nil)
	}
	if ctx.AgentName() == a.entrypoint {
		return nil, nil
	}
	scope, ok := a.scope(ctx.SessionID())
	if !ok {
		return nil, domain.NewError(domain.CodeAuditSessionRequired, "delegation has no audit scope", nil)
	}
	record, err := a.startedRecord(ctx, scope)
	if err != nil {
		return nil, err
	}
	key := a.pendingKey(ctx)
	a.mu.Lock()
	a.pending[key] = record
	a.mu.Unlock()
	if err := a.record(ctx, scope, record, audit.EventDelegationStarted); err != nil {
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
		return nil, err
	}
	return nil, nil
}

// AfterAgent records a successful delegated Agent invocation. Agent failures
// that prevent AfterAgent are reconciled by FailPending when the run ends.
func (a *DelegationAuditor) AfterAgent(ctx adkagent.Context) (*genai.Content, error) {
	if ctx == nil {
		return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, "agent callback context is required", nil)
	}
	if ctx.AgentName() == a.entrypoint {
		return nil, nil
	}
	scope, ok := a.scope(ctx.SessionID())
	if !ok {
		return nil, domain.NewError(domain.CodeAuditSessionRequired, "delegation has no audit scope", nil)
	}
	key := a.pendingKey(ctx)
	a.mu.Lock()
	record, ok := a.pending[key]
	a.mu.Unlock()
	if !ok {
		return nil, domain.NewError(domain.CodeDelegationUnknown, "agent completion has no matching started audit", fmt.Errorf("agent %q invocation %q", ctx.AgentName(), ctx.InvocationID()))
	}
	return nil, a.complete(ctx, scope, key, record)
}

// CompleteByToolResponse closes the pending delegation whose target Agent
// produced this synthesized function response. ADK stops a task-mode
// sub-agent's event iterator as soon as its finish_task call succeeds, so the
// sub-agent's AfterAgent callback never runs; the synthesized task-tool
// function response (tool name == sub-agent name) is the framework's
// completion signal for that delegation.
func (a *DelegationAuditor) CompleteByToolResponse(
	ctx context.Context,
	scope *runExecution,
	event *session.Event,
) error {
	if event == nil || event.Content == nil {
		return nil
	}
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionResponse == nil {
			continue
		}
		target := part.FunctionResponse.Name
		a.mu.Lock()
		key, record, ok := a.pendingForTarget(target)
		a.mu.Unlock()
		if !ok {
			continue
		}
		if err := a.complete(ctx, scope, key, record); err != nil {
			return err
		}
	}
	return nil
}

// pendingForTarget returns the oldest pending delegation record for a target
// Agent. Task delegations are sequential (ADK forbids parallel task calls), so
// at most one record per target is in flight.
func (a *DelegationAuditor) pendingForTarget(target string) (string, DelegationExecutionRecord, bool) {
	for key, record := range a.pending {
		if record.TargetAgent == target {
			return key, record, true
		}
	}
	return "", DelegationExecutionRecord{}, false
}

// complete appends the succeeded terminal evidence for one delegation and
// releases its pending record.
func (a *DelegationAuditor) complete(
	ctx context.Context,
	scope *runExecution,
	key string,
	record DelegationExecutionRecord,
) error {
	finishedAt := a.now()
	record, err := completeDelegation(record, finishedAt)
	if err != nil {
		return err
	}
	if auditErr := a.record(ctx, scope, record, audit.EventDelegationCompleted); auditErr != nil {
		return auditErr
	}
	a.mu.Lock()
	delete(a.pending, key)
	a.mu.Unlock()
	if a.metrics != nil {
		a.metrics.ObserveAgentDelegation(record.CallerAgent, record.TargetAgent, string(record.Status), time.Duration(record.DurationMS)*time.Millisecond)
	}
	return nil
}

// FailPending reconciles delegated invocations that never reached AfterAgent
// because the run failed or was cancelled. A record is dequeued only after its
// terminal evidence is durably appended, so a transient failure stays
// retryable by the run's cleanup callback.
func (a *DelegationAuditor) FailPending(ctx context.Context, scope *runExecution) error {
	a.mu.Lock()
	pending := make(map[string]DelegationExecutionRecord)
	for key, record := range a.pending {
		if scope == nil || string(record.RunID) == string(scope.identity.RunID) {
			pending[key] = record
		}
	}
	a.mu.Unlock()

	var firstErr error
	for key, record := range pending {
		record, lifecycleErr := failDelegation(
			record,
			a.now(),
			domain.CodeDelegationFailed,
			"agent delegation interrupted by run end",
		)
		if lifecycleErr != nil {
			if firstErr == nil {
				firstErr = lifecycleErr
			}
			continue
		}
		if auditErr := a.record(ctx, scope, record, audit.EventDelegationFailed); auditErr != nil {
			if firstErr == nil {
				firstErr = auditErr
			}
			continue
		}
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
		if a.metrics != nil {
			a.metrics.ObserveAgentDelegation(record.CallerAgent, record.TargetAgent, string(record.Status), time.Duration(record.DurationMS)*time.Millisecond)
		}
	}
	return firstErr
}

func (a *DelegationAuditor) startedRecord(
	ctx adkagent.Context,
	scope *runExecution,
) (DelegationExecutionRecord, error) {
	target := ctx.AgentName()
	definition, ok := a.agents[target]
	if !ok {
		return DelegationExecutionRecord{}, domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("delegated agent %q is not deployed", target), nil)
	}
	definitionDigest := definition.Digest
	path := strings.TrimSpace(ctx.Branch())
	if path == "" {
		path = target
	}
	caller, depth := callerFromAgentPath(path, a.entrypoint)
	return DelegationExecutionRecord{
		RootRunID:             scope.identity.RunID,
		RunID:                 scope.identity.RunID,
		ThreadID:              scope.identity.ThreadID,
		UserID:                scope.identity.UserID,
		CallerAgent:           caller,
		TargetAgent:           target,
		AgentDefinitionDigest: definitionDigest,
		AgentPath:             path,
		Depth:                 depth,
		Status:                audit.StatusRunning,
		StartedAt:             a.now(),
	}, nil
}

func (a *DelegationAuditor) record(ctx context.Context, scope *runExecution, record DelegationExecutionRecord, eventType audit.EventType) error {
	target, ok := a.agents[record.TargetAgent]
	if !ok {
		return domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("delegated agent %q is not deployed", record.TargetAgent), nil)
	}
	status := audit.StatusRunning
	switch eventType {
	case audit.EventDelegationCompleted:
		status = audit.StatusSucceeded
	case audit.EventDelegationFailed:
		status = audit.StatusFailed
	}
	sc := trace.SpanContextFromContext(ctx)
	event := scopedEvent(scope)
	event.ID = fmt.Sprintf("%s/%s:%s", scope.identity.RunID, record.AgentPath, eventType)
	event.Type = eventType
	event.TraceID = sc.TraceID().String()
	event.SpanID = sc.SpanID().String()
	event.AgentName = record.TargetAgent
	event.AgentVersion = target.Version
	event.AgentDefinitionDigest = target.Digest
	event.CallerAgent = record.CallerAgent
	event.TargetAgent = record.TargetAgent
	event.DelegationDepth = record.Depth
	event.ParentRunID = scope.identity.RunID
	event.Status = status
	event.DurationMS = record.DurationMS
	event.ErrorCode = record.ErrorCode
	event.ErrorMessage = record.ErrorMessage
	event.Payload = map[string]any{
		"agent_path": record.AgentPath,
	}
	if eventType != audit.EventDelegationStarted {
		event.OccurredAt = record.FinishedAt
	} else {
		event.OccurredAt = record.StartedAt
	}
	event, err := audit.ValidateEvent(event)
	if err != nil {
		return err
	}
	return a.recorder.Record(context.WithoutCancel(ctx), &event)
}

func (a *DelegationAuditor) pendingKey(ctx adkagent.Context) string {
	return ctx.SessionID() + "\x00" + ctx.Branch() + "\x00" + ctx.AgentName() + "\x00" + ctx.InvocationID()
}

func callerFromAgentPath(path, entrypoint string) (string, int) {
	path = strings.TrimSpace(path)
	if path == "" {
		return entrypoint, 0
	}
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		return entrypoint, 0
	}
	depth := len(parts) - 1
	return parts[len(parts)-2], depth
}
