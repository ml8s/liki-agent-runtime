package agent

import "github.com/ml8s/liki-agent-runtime/internal/audit"

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
		DefinitionName:    scope.definitionName,
		DefinitionVersion: scope.definitionVersion,
		DefinitionDigest:  scope.definitionDigest,
	}
}
