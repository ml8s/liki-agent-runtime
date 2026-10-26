package audit

import "time"

// AppendMetrics observes audit persistence outcomes without exposing the
// storage adapter. Implementations must be safe for concurrent use.
type AppendMetrics interface {
	ObserveAuditAppend(status string, duration time.Duration)
}

// AppendBatchMetrics is an optional AppendMetrics capability that observes how
// many events share one grouped commit; it verifies group-commit behavior in
// production.
type AppendBatchMetrics interface {
	ObserveAuditAppendBatch(eventsPerCommit int)
}

// QueueDepthMetrics is an optional AppendMetrics capability that observes the
// audit writer's pending backlog.
type QueueDepthMetrics interface {
	ObserveAuditQueueDepth(depth int)
}

// Stable append outcome labels for the metrics port. Storage adapters must map
// their internal failures onto this set so dashboards stay adapter-neutral.
const (
	AppendStatusOK       = "ok"
	AppendStatusConflict = "conflict"
	AppendStatusBusy     = "busy"
	AppendStatusError    = "error"
	AppendStatusInvalid  = "invalid"
	AppendStatusRejected = "rejected"
)
