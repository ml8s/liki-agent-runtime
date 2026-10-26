package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"gorm.io/gorm"
)

// AuditEventRepository persists immutable audit events.
type AuditEventRepository struct {
	db      *gorm.DB
	metrics audit.AppendMetrics
}

// recoveryWindow bounds the startup orphan scan. Every successful startup
// closes all orphans, so at the next startup an unclosed lifecycle can only
// have been created since the previous recovery; the week of margin absorbs
// clock skew and restored databases while keeping the scan index-bounded.
const recoveryWindow = 7 * 24 * time.Hour

// RepositoryOption customizes an audit repository.
type RepositoryOption func(*AuditEventRepository)

// WithMetrics observes every append outcome and its persistence latency.
func WithMetrics(metrics audit.AppendMetrics) RepositoryOption {
	return func(repository *AuditEventRepository) {
		repository.metrics = metrics
	}
}

// NewAuditEventRepository builds an audit repository over a database handle.
func NewAuditEventRepository(db *gorm.DB, options ...RepositoryOption) *AuditEventRepository {
	repository := &AuditEventRepository{db: db}
	for _, option := range options {
		option(repository)
	}
	return repository
}

// Record appends one validated audit event, treating a retry of an identical
// event as idempotent.
func (r *AuditEventRepository) Record(ctx context.Context, event *audit.Event) error {
	started := time.Now()
	status, err := r.record(ctx, event)
	r.observeAuditAppend(status, time.Since(started))
	return err
}

func (r *AuditEventRepository) record(ctx context.Context, event *audit.Event) (string, error) {
	if event == nil {
		return audit.AppendStatusInvalid, audit.NewError(
			audit.CodeAuditAppendFailed,
			"audit event is required",
			domain.ErrInvalidInput,
		)
	}
	if err := event.Validate(); err != nil {
		return audit.AppendStatusInvalid, err
	}
	return r.append(ctx, r.db, event)
}

func (r *AuditEventRepository) observeAuditAppend(status string, duration time.Duration) {
	if r.metrics != nil {
		r.metrics.ObserveAuditAppend(status, duration)
	}
}

// appendEvent inserts one audit event on the supplied handle or transaction
// and reports the stable append outcome. A UNIQUE failure aborts only this
// statement in SQLite, so a caller may continue the surrounding transaction
// with other events after a conflict.
func appendEvent(ctx context.Context, db *gorm.DB, event *audit.Event) (string, error) {
	model, err := newAuditEventModel(event)
	if err != nil {
		return audit.AppendStatusError, err
	}
	if err := db.WithContext(ctx).Create(&model).Error; err != nil {
		if isBusyAppendError(err) {
			return audit.AppendStatusBusy, audit.NewError(audit.CodeAuditAppendFailed, "append audit event", err)
		}
		var existing auditEventModel
		findErr := db.WithContext(ctx).Where("id = ?", event.ID).First(&existing).Error
		if findErr == nil && existing == model {
			// A retry after a committed-but-unacknowledged append is idempotent.
			return audit.AppendStatusOK, nil
		}
		if findErr == nil {
			return audit.AppendStatusConflict, audit.NewError(audit.CodeAuditEventConflict, "audit event id already exists", err)
		}
		if isBusyAppendError(findErr) {
			return audit.AppendStatusBusy, audit.NewError(audit.CodeAuditAppendFailed, "append audit event", err)
		}
		return audit.AppendStatusError, audit.NewError(audit.CodeAuditAppendFailed, "append audit event", err)
	}
	return audit.AppendStatusOK, nil
}

func (r *AuditEventRepository) append(ctx context.Context, db *gorm.DB, event *audit.Event) (string, error) {
	return appendEvent(ctx, db, event)
}

// isBusyAppendError recognizes SQLite writer contention through the driver's
// stable error text. It stays inside the adapter so callers only see the
// coded audit error and the metric status.
func isBusyAppendError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "SQLITE_BUSY") || strings.Contains(message, "database is locked")
}

// RecoverInterrupted appends terminal failure evidence for every started audit
// lifecycle that has no completed or failed event. It runs before the runtime
// accepts traffic and relies on the stable event-ID rule; malformed legacy
// IDs fail closed instead of being silently transformed.
func (r *AuditEventRepository) RecoverInterrupted(ctx context.Context, now time.Time) error {
	return r.recoverInterrupted(ctx, r.db, now)
}

func (r *AuditEventRepository) recoverInterrupted(ctx context.Context, db *gorm.DB, now time.Time) error {
	if now.IsZero() {
		return audit.NewError(audit.CodeAuditAppendFailed, "recovery timestamp is required", nil)
	}

	failureTypes := map[string]string{
		string(audit.EventRunStarted):        string(audit.EventRunFailed),
		string(audit.EventLLMCallStarted):    string(audit.EventLLMCallFailed),
		string(audit.EventToolCallStarted):   string(audit.EventToolCallFailed),
		string(audit.EventDelegationStarted): string(audit.EventDelegationFailed),
	}
	startedNames := make([]string, 0, len(failureTypes))
	for name := range failureTypes {
		startedNames = append(startedNames, name)
	}
	// Derive terminal IDs from the final lifecycle suffix so SQLite can use the
	// primary-key index and return only true orphans instead of loading history.
	orphanFilter := `
event_type IN ?
AND agent_audit_events.occurred_at >= ?
AND NOT EXISTS (
	SELECT 1
	FROM agent_audit_events AS completed
	WHERE completed.id = substr(agent_audit_events.id, 1, length(agent_audit_events.id) - length(agent_audit_events.event_type) - 1) || ':' || replace(agent_audit_events.event_type, '.started', '.completed')
	  AND completed.event_type = replace(agent_audit_events.event_type, '.started', '.completed')
)
AND NOT EXISTS (
	SELECT 1
	FROM agent_audit_events AS failed
	WHERE failed.id = substr(agent_audit_events.id, 1, length(agent_audit_events.id) - length(agent_audit_events.event_type) - 1) || ':' || replace(agent_audit_events.event_type, '.started', '.failed')
	  AND failed.event_type = replace(agent_audit_events.event_type, '.started', '.failed')
)`

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var interrupted []auditEventModel
		if err := tx.WithContext(ctx).
			Where(orphanFilter, startedNames, now.Add(-recoveryWindow)).
			Order("occurred_at ASC, id ASC").
			Find(&interrupted).Error; err != nil {
			return audit.NewError(audit.CodeAuditAppendFailed, "find interrupted audit events", err)
		}

		for _, original := range interrupted {
			base, ok := strings.CutSuffix(original.ID, ":"+original.EventType)
			if !ok || base == "" {
				return audit.NewError(
					audit.CodeAuditAppendFailed,
					fmt.Sprintf("audit event %q does not follow the stable lifecycle ID rule", original.ID),
					nil,
				)
			}
			failureType := failureTypes[original.EventType]
			failedID := base + ":" + failureType

			event, err := recoveryEvent(original, failureType, failedID, now)
			if err != nil {
				return err
			}
			if _, err := r.append(ctx, tx, event); err != nil {
				return fmt.Errorf("recover %q: %w", original.ID, err)
			}
		}
		return nil
	})
}

func recoveryEvent(original auditEventModel, terminalType, id string, now time.Time) (*audit.Event, error) {
	payload := map[string]any{}
	if strings.TrimSpace(original.PayloadJSON) != "" {
		if err := json.Unmarshal([]byte(original.PayloadJSON), &payload); err != nil {
			return nil, audit.NewError(audit.CodeAuditAppendFailed, "decode original audit payload", err)
		}
	}
	payload["recovery"] = "startup"
	payload["original_event_id"] = original.ID

	duration := now.Sub(original.OccurredAt).Milliseconds()
	if duration < 0 {
		return nil, audit.NewError(
			audit.CodeAuditAppendFailed,
			fmt.Sprintf("recovery timestamp precedes audit event %q", original.ID),
			nil,
		)
	}

	event := &audit.Event{
		ID:                    id,
		Type:                  audit.EventType(terminalType),
		OccurredAt:            now,
		RootRunID:             domain.ID(original.RootRunID),
		RunID:                 domain.ID(original.RunID),
		ParentRunID:           domain.ID(original.ParentRunID),
		ThreadID:              domain.ID(original.ThreadID),
		UserID:                original.UserID,
		Protocol:              original.Protocol,
		TraceID:               original.TraceID,
		SpanID:                original.SpanID,
		AgentName:             original.AgentName,
		DefinitionName:        original.DefinitionName,
		DefinitionVersion:     original.DefinitionVersion,
		DefinitionDigest:      original.DefinitionDigest,
		AgentVersion:          original.AgentVersion,
		AgentDefinitionDigest: original.AgentDefinitionDigest,
		CallerAgent:           original.CallerAgent,
		TargetAgent:           original.TargetAgent,
		DelegationDepth:       original.DelegationDepth,
		ToolCallID:            original.ToolCallID,
		ToolName:              original.ToolName,
		Model:                 original.Model,
		Provider:              original.Provider,
		Status:                audit.StatusFailed,
		DurationMS:            duration,
		ErrorCode:             domain.CodeRuntimeInterrupted,
		ErrorMessage:          "process interruption detected during startup recovery",
		Payload:               payload,
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	return event, nil
}

// RunExists reports whether any audit evidence exists for the run id.
func (r *AuditEventRepository) RunExists(ctx context.Context, runID domain.ID) (bool, error) {
	// The run_id index answers an existence probe with one bounded lookup; an
	// aggregate COUNT would keep scanning matching rows instead.
	var exists int
	if err := r.db.WithContext(ctx).
		Raw("SELECT 1 FROM agent_audit_events WHERE run_id = ? LIMIT 1", string(runID)).
		Scan(&exists).Error; err != nil {
		return false, audit.NewError(audit.CodeAuditAppendFailed, "check run existence", err)
	}
	return exists == 1, nil
}
