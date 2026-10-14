package agent

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

// AgentReferenceAuditor instruments ADK's standard BeforeAgent and
// AfterAgent callbacks. The entrypoint is excluded because Runtime.Run already
// records its run lifecycle.
type AgentReferenceAuditor struct {
	recorder   audit.Recorder
	metrics    Metrics
	now        func() time.Time
	entrypoint string
	agents     map[string]*AgentDefinition
	scope      func(sessionID string) (*llmRunScope, bool)

	mu      sync.Mutex
	pending map[string]DelegationExecutionRecord
}

func newAgentReferenceAuditor(
	recorder audit.Recorder,
	metrics Metrics,
	now func() time.Time,
	deployment *Deployment,
	entrypoint string,
	scope func(sessionID string) (*llmRunScope, bool),
) *AgentReferenceAuditor {
	agents := make(map[string]*AgentDefinition, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		definition := &deployment.Spec.Agents[index]
		agents[definition.Name] = definition
	}
	return &AgentReferenceAuditor{
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
func (a *AgentReferenceAuditor) BeforeAgent(ctx adkagent.Context) (*genai.Content, error) {
	if ctx == nil {
		return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, "agent callback context is required", nil)
	}
	if ctx.AgentName() == a.entrypoint {
		return nil, nil
	}
	record, err := a.startedRecord(ctx)
	if err != nil {
		return nil, err
	}
	scope, ok := a.scope(ctx.SessionID())
	if !ok {
		return nil, domain.NewError(domain.CodeAuditSessionRequired, "delegation has no audit scope", nil)
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
func (a *AgentReferenceAuditor) AfterAgent(ctx adkagent.Context) (*genai.Content, error) {
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

	finishedAt := a.now()
	record.FinishedAt = finishedAt
	record.DurationMS = finishedAt.Sub(record.StartedAt).Milliseconds()
	record.Status = audit.StatusSucceeded
	if err := a.record(ctx, scope, record, audit.EventDelegationCompleted); err != nil {
		return nil, err
	}
	a.mu.Lock()
	delete(a.pending, key)
	a.mu.Unlock()
	if a.metrics != nil {
		a.metrics.ObserveAgentDelegation(record.CallerAgent, record.TargetAgent, string(record.Status), time.Duration(record.DurationMS)*time.Millisecond)
	}
	return nil, nil
}

// FailPending reconciles delegated invocations that never reached AfterAgent
// because the run failed or was cancelled. A record is dequeued only after its
// terminal evidence is durably appended, so a transient failure stays
// retryable by the run's cleanup callback.
func (a *AgentReferenceAuditor) FailPending(ctx context.Context, scope *llmRunScope) error {
	a.mu.Lock()
	pending := make(map[string]DelegationExecutionRecord)
	for key, record := range a.pending {
		if scope == nil || string(record.RunID) == string(scope.runID) {
			pending[key] = record
		}
	}
	a.mu.Unlock()

	var firstErr error
	for key, record := range pending {
		record.FinishedAt = a.now()
		record.DurationMS = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		record.Status = audit.StatusFailed
		record.ErrorCode = domain.CodeDelegationFailed
		record.ErrorMessage = "agent delegation interrupted by run end"
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

func (a *AgentReferenceAuditor) startedRecord(ctx adkagent.Context) (DelegationExecutionRecord, error) {
	scope, ok := a.scope(ctx.SessionID())
	if !ok {
		return DelegationExecutionRecord{}, domain.NewError(domain.CodeAuditSessionRequired, "delegation has no audit scope", nil)
	}
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
		RootRunID:             scope.runID,
		RunID:                 scope.runID,
		ThreadID:              scope.threadID,
		UserID:                scope.userID,
		CallerAgent:           caller,
		TargetAgent:           target,
		AgentDefinitionDigest: definitionDigest,
		AgentPath:             path,
		Depth:                 depth,
		Status:                audit.StatusRunning,
		StartedAt:             a.now(),
	}, nil
}

func (a *AgentReferenceAuditor) record(ctx context.Context, scope *llmRunScope, record DelegationExecutionRecord, eventType audit.EventType) error {
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
	event.ID = fmt.Sprintf("%s/%s:%s", scope.runID, record.AgentPath, eventType)
	event.Type = eventType
	event.TraceID = sc.TraceID().String()
	event.SpanID = sc.SpanID().String()
	event.AgentName = record.TargetAgent
	event.AgentVersion = target.Version
	event.AgentDefinitionDigest = target.Digest
	event.CallerAgent = record.CallerAgent
	event.TargetAgent = record.TargetAgent
	event.DelegationDepth = record.Depth
	event.ParentRunID = scope.runID
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
	if err := event.Validate(); err != nil {
		return err
	}
	return a.recorder.Record(context.WithoutCancel(ctx), &event)
}

func (a *AgentReferenceAuditor) pendingKey(ctx adkagent.Context) string {
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
