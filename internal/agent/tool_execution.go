package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// ToolExecutionRecord is the runtime evidence for one standard ADK tool call.
// Raw arguments and results never enter audit; only canonical digests and size
// are retained.
type ToolExecutionRecord struct {
	CallID       string
	Tool         string
	RootRunID    domain.ID
	RunID        domain.ID
	ThreadID     domain.ID
	UserID       string
	AgentName    string
	AgentVersion string
	MCPServer    string
	Status       audit.Status
	StartedAt    time.Time
	FinishedAt   time.Time
	DurationMS   int64
	InputDigest  string
	InputBytes   int64
	OutputDigest string
	OutputBytes  int64
	ErrorCode    string
	ErrorMessage string
}

// ToolExecutionAuditor instruments ADK's standard tool callbacks. It records
// evidence for every LLM Agent in the deployment, including delegated agents.
type ToolExecutionAuditor struct {
	recorder audit.Recorder
	metrics  Metrics
	now      func() time.Time
	provider string

	agents map[string]*AgentDefinition
	scope  func(sessionID string) (*runScope, bool)

	mu      sync.Mutex
	pending map[string]ToolExecutionRecord
}

func newToolExecutionAuditor(
	recorder audit.Recorder,
	metrics Metrics,
	now func() time.Time,
	provider string,
	deployment *Deployment,
	scope func(sessionID string) (*runScope, bool),
) *ToolExecutionAuditor {
	agents := make(map[string]*AgentDefinition, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		definition := &deployment.Spec.Agents[index]
		agents[definition.Name] = definition
	}
	return &ToolExecutionAuditor{
		recorder: recorder,
		metrics:  metrics,
		now:      now,
		provider: provider,
		agents:   agents,
		scope:    scope,
		pending:  make(map[string]ToolExecutionRecord),
	}
}

// isFrameworkDelegationTool reports whether an ADK tool call is framework
// delegation plumbing rather than an MCP tool call: the built-in
// transfer_to_agent, the task / single_turn tool ADK installs on a parent for
// a sub-agent (the tool is named after the sub-agent), and the finish_task
// tool ADK installs on a task Agent. These carry delegation provenance through
// Agent lifecycle events and are exempt from MCP tool allowlist auditing.
func (a *ToolExecutionAuditor) isFrameworkDelegationTool(agentName, toolName string) bool {
	if toolName == adkTransferToolName {
		return true
	}
	definition, ok := a.agents[agentName]
	if !ok {
		return false
	}
	if toolName == adkFinishTaskToolName {
		return definition.Mode == AgentModeTask
	}
	for _, reference := range definition.SubAgents {
		if reference.Name != toolName {
			continue
		}
		target, ok := a.agents[reference.Name]
		return ok && (target.Mode == AgentModeTask || target.Mode == AgentModeSingleTurn)
	}
	return false
}

// BeforeTool records the started fact. Returning nil tells ADK to continue
// normal tool execution; returning args would replace the tool with its input.
func (a *ToolExecutionAuditor) BeforeTool(
	ctx adkagent.Context,
	tool tool.Tool,
	args map[string]any,
) (map[string]any, error) {
	if tool != nil && tool.Name() == adkTransferToolName {
		return nil, nil
	}
	if ctx != nil && tool != nil && a.isFrameworkDelegationTool(ctx.AgentName(), tool.Name()) {
		return nil, nil
	}
	if ctx == nil || tool == nil {
		return args, domain.NewError(domain.CodeToolCallInvalid, "tool callback context and tool are required", nil)
	}
	scope, ok := a.scope(ctx.SessionID())
	if !ok {
		return args, domain.NewError(domain.CodeAuditSessionRequired, "tool execution has no audit scope", nil)
	}
	record, err := a.startedRecord(ctx, tool, args, scope)
	if err != nil {
		return args, err
	}
	key := a.pendingKey(ctx, record.CallID)
	a.mu.Lock()
	a.pending[key] = record
	a.mu.Unlock()
	if auditErr := a.record(ctx, scope, record, audit.EventToolCallStarted); auditErr != nil {
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
		return args, auditErr
	}
	return nil, nil
}

// AfterTool records the terminal fact and preserves ADK's result and error.
func (a *ToolExecutionAuditor) AfterTool(
	ctx adkagent.Context,
	tool tool.Tool,
	_ map[string]any,
	result map[string]any,
	err error,
) (map[string]any, error) {
	if tool != nil && tool.Name() == adkTransferToolName {
		return result, nil
	}
	if ctx != nil && tool != nil && a.isFrameworkDelegationTool(ctx.AgentName(), tool.Name()) {
		return result, nil
	}
	if ctx == nil || tool == nil {
		return result, domain.NewError(domain.CodeToolCallInvalid, "tool callback context and tool are required", nil)
	}
	scope, ok := a.scope(ctx.SessionID())
	if !ok {
		return result, domain.NewError(domain.CodeAuditSessionRequired, "tool execution has no audit scope", nil)
	}
	key := a.pendingKey(ctx, ctx.FunctionCallID())
	a.mu.Lock()
	record, ok := a.pending[key]
	a.mu.Unlock()
	if !ok {
		return result, domain.NewError(domain.CodeToolCallUnknown, "tool completion has no matching started audit", fmt.Errorf("tool %q call id %q", tool.Name(), ctx.FunctionCallID()))
	}

	finishedAt := a.now()
	record.FinishedAt = finishedAt
	record.DurationMS = finishedAt.Sub(record.StartedAt).Milliseconds()
	outputDigest, outputBytes, digestErr := audit.CanonicalDigest(result)
	if digestErr != nil {
		return result, domain.NewError(domain.CodeToolProvenanceInvalid, "digest tool result", digestErr)
	}
	record.OutputDigest = outputDigest
	record.OutputBytes = outputBytes
	if err != nil {
		record.Status = audit.StatusFailed
		record.ErrorCode = domain.CodeToolExecutionFailed
		record.ErrorMessage = "MCP tool execution failed"
	} else {
		record.Status = audit.StatusSucceeded
	}
	eventType := audit.EventToolCallCompleted
	if record.Status == audit.StatusFailed {
		eventType = audit.EventToolCallFailed
	}
	if auditErr := a.record(ctx, scope, record, eventType); auditErr != nil {
		// Keep the started invocation pending so Runtime.Run's terminal
		// reconciliation can emit a terminal event after a transient failure.
		return result, auditErr
	}
	a.mu.Lock()
	// Another callback cannot legitimately complete this call ID first; delete
	// only after its terminal evidence has been accepted.
	delete(a.pending, key)
	a.mu.Unlock()
	if a.metrics != nil {
		a.metrics.ObserveToolCall(record.AgentName, record.Tool, string(record.Status), time.Duration(record.DurationMS)*time.Millisecond)
	}
	return result, err
}

// FailPending reconciles tool invocations that never reached AfterTool because
// the run failed, was cancelled, or its event stream terminated early. A record
// is dequeued only after its terminal evidence is durably appended, so a
// transient failure stays retryable by the run's cleanup callback.
func (a *ToolExecutionAuditor) FailPending(
	ctx context.Context,
	scope *runScope,
) error {
	a.mu.Lock()
	pending := make(map[string]ToolExecutionRecord)
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
		record.ErrorCode = domain.CodeToolExecutionInterrupted
		record.ErrorMessage = "tool execution interrupted by run end"
		if auditErr := a.record(ctx, scope, record, audit.EventToolCallFailed); auditErr != nil {
			if firstErr == nil {
				firstErr = auditErr
			}
			continue
		}
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
		if a.metrics != nil {
			a.metrics.ObserveToolCall(record.AgentName, record.Tool, string(record.Status), time.Duration(record.DurationMS)*time.Millisecond)
		}
	}
	return firstErr
}

func (a *ToolExecutionAuditor) startedRecord(
	ctx adkagent.Context,
	tool tool.Tool,
	args map[string]any,
	scope *runScope,
) (ToolExecutionRecord, error) {
	record := ToolExecutionRecord{
		RootRunID: scope.runID,
		RunID:     scope.runID,
		ThreadID:  scope.threadID,
		UserID:    scope.userID,
	}
	definition, ok := a.agents[ctx.AgentName()]
	if !ok {
		return ToolExecutionRecord{}, domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("tool execution agent %q is not deployed", ctx.AgentName()), nil)
	}
	reference, allowed := definition.ToolReferenceFor(tool.Name())
	if !allowed {
		return ToolExecutionRecord{}, domain.NewError(domain.CodeToolCallInvalid, fmt.Sprintf("tool %q is not allowlisted for agent %q", tool.Name(), ctx.AgentName()), nil)
	}
	inputDigest, inputBytes, err := audit.CanonicalDigest(args)
	if err != nil {
		return ToolExecutionRecord{}, domain.NewError(domain.CodeToolProvenanceInvalid, "digest tool input", err)
	}
	callID := ctx.FunctionCallID()
	if callID == "" {
		return ToolExecutionRecord{}, domain.NewError(domain.CodeToolCallIDRequired, "tool function call id is required", nil)
	}
	record.CallID = callID
	record.Tool = tool.Name()
	record.AgentName = ctx.AgentName()
	record.AgentVersion = definition.Version
	record.MCPServer = reference.Server
	record.Status = audit.StatusRunning
	record.StartedAt = a.now()
	record.InputDigest = inputDigest
	record.InputBytes = inputBytes
	return record, nil
}

// record appends one tool-call lifecycle event. It serves both the ADK
// callbacks (started/completed/failed) and end-of-run reconciliation, so a
// missing scope fails closed instead of dropping evidence.
func (a *ToolExecutionAuditor) record(
	ctx context.Context,
	scope *runScope,
	record ToolExecutionRecord,
	eventType audit.EventType,
) error {
	if scope == nil {
		return domain.NewError(domain.CodeAuditSessionRequired, "tool audit has no run scope", nil)
	}
	definition, ok := a.agents[record.AgentName]
	if !ok {
		return domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("tool execution agent %q is not deployed", record.AgentName), nil)
	}
	status := audit.StatusRunning
	switch eventType {
	case audit.EventToolCallCompleted:
		status = audit.StatusSucceeded
	case audit.EventToolCallFailed:
		status = audit.StatusFailed
	}
	event := scopedEvent(scope)
	event.ID = fmt.Sprintf("%s/%s/%s:%s", scope.runID, record.AgentName, record.CallID, eventType)
	event.Type = eventType
	event.AgentName = record.AgentName
	event.AgentVersion = record.AgentVersion
	event.AgentDefinitionDigest = definition.Digest
	event.ToolCallID = record.CallID
	event.ToolName = record.Tool
	event.Model = scope.model
	event.Provider = a.provider
	event.Status = status
	event.TraceID = traceIDFromContext(ctx)
	event.SpanID = spanIDFromContext(ctx)
	event.DurationMS = record.DurationMS
	event.ErrorCode = record.ErrorCode
	event.ErrorMessage = record.ErrorMessage
	event.Payload = map[string]any{
		"mcp_server":    record.MCPServer,
		"input_digest":  record.InputDigest,
		"input_bytes":   record.InputBytes,
		"output_digest": record.OutputDigest,
		"output_bytes":  record.OutputBytes,
	}
	if eventType == audit.EventToolCallStarted {
		event.OccurredAt = record.StartedAt
	} else {
		event.OccurredAt = record.FinishedAt
	}
	if err := event.Validate(); err != nil {
		return err
	}
	return a.recorder.Record(context.WithoutCancel(ctx), &event)
}

func (a *ToolExecutionAuditor) pendingKey(ctx adkagent.Context, callID string) string {
	return ctx.SessionID() + "\x00" + callID
}
