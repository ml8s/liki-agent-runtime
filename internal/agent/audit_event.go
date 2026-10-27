package agent

import (
	"context"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

// scopedEvent seeds an audit event with the provenance fields that every
// lifecycle event of a run shares. Callers then set the lifecycle and
// event-specific fields (identity, type, status, timing, error, payload).
//
// Keeping this in one place means a new shared provenance field is added once
// instead of at every event-construction site.
func scopedEvent(scope *runExecution) audit.Event {
	return audit.Event{
		RootRunID:         scope.identity.RunID,
		RunID:             scope.identity.RunID,
		ThreadID:          scope.identity.ThreadID,
		UserID:            scope.identity.UserID,
		Protocol:          scope.provenance.Protocol,
		ParentRunID:       domain.ID(scope.taskID),
		DefinitionName:    scope.definitionName,
		DefinitionVersion: scope.definitionVersion,
		DefinitionDigest:  scope.definitionDigest,
	}
}

// scopedTerminalEvent extends scopedEvent with the fields that every
// auditor's record() populates identically: event type, derived status,
// and the live trace identity from the request context.
func scopedTerminalEvent(ctx context.Context, scope *runExecution, eventType audit.EventType) audit.Event {
	event := scopedEvent(scope)
	event.Type = eventType
	event.Status = audit.StatusFor(eventType)
	event.TraceID = traceIDFromContext(ctx)
	event.SpanID = spanIDFromContext(ctx)
	return event
}
