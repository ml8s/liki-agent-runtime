package agent

import "github.com/ml8s/liki-agents/internal/audit"

// scopedEvent seeds an audit event with the provenance fields that every
// lifecycle event of a run shares. Callers then set the lifecycle and
// event-specific fields (identity, type, status, timing, error, payload).
//
// Keeping this in one place means a new shared provenance field is added once
// instead of at every event-construction site.
func scopedEvent(scope *llmRunScope) audit.Event {
	return audit.Event{
		RootRunID:         scope.runID,
		RunID:             scope.runID,
		ThreadID:          scope.threadID,
		UserID:            scope.userID,
		Protocol:          scope.protocol,
		DefinitionName:    scope.definitionName,
		DefinitionVersion: scope.definitionVersion,
		DefinitionDigest:  scope.definitionDigest,
	}
}
