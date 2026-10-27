package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

func TestAuditEventRepositoryAppendsImmutableEvents(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent-audit.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})

	recorder := NewAuditEventRepository(store.GORM())
	ctx := context.Background()
	info, err := os.Stat(databasePath)
	if err != nil {
		t.Fatalf("stat sqlite database: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sqlite permissions = %o, want 600", info.Mode().Perm())
	}
	occurred := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	started := audit.Event{
		ID:   "call_1:started",
		Type: audit.EventLLMCallStarted, OccurredAt: occurred,
		RootRunID: "run_1", RunID: "run_1", ThreadID: "thread_1",
		UserID: "user_1", AgentName: "coordinator",
		Model: "test-model", Provider: "test", Status: audit.StatusRunning,
	}
	if _, err := audit.ValidateEvent(started); err != nil {
		t.Fatalf("validate started event: %v", err)
	}
	exists, err := recorder.RunExists(ctx, started.RunID)
	if err != nil {
		t.Fatalf("RunExists(empty) error = %v", err)
	}
	if exists {
		t.Fatal("RunExists(empty) unexpectedly returned true")
	}
	if err := recorder.Record(ctx, &started); err != nil {
		t.Fatalf("Record(started) error = %v", err)
	}
	exists, err = recorder.RunExists(ctx, started.RunID)
	if err != nil {
		t.Fatalf("RunExists(recorded) error = %v", err)
	}
	if !exists {
		t.Fatal("RunExists(recorded) returned false")
	}

	completed := started
	completed.ID = "call_1:completed"
	completed.Type = audit.EventLLMCallCompleted
	completed.OccurredAt = occurred.Add(time.Second)
	completed.Status = audit.StatusSucceeded
	completed.DurationMS = 1000
	if err := recorder.Record(ctx, &completed); err != nil {
		t.Fatalf("Record(completed) error = %v", err)
	}
	if err := recorder.Record(ctx, &completed); err != nil {
		t.Fatalf("Record(idempotent completed) error = %v", err)
	}
	conflict := completed
	conflict.Type = audit.EventLLMCallFailed
	conflict.Status = audit.StatusFailed
	conflict.ErrorCode = domain.CodeLLMCallFailed
	err = recorder.Record(ctx, &conflict)
	var conflictErr *domain.Error
	if !errors.As(err, &conflictErr) || conflictErr.Code != audit.CodeAuditEventConflict {
		t.Fatalf("Record(conflicting id) error = %v, want %s", err, audit.CodeAuditEventConflict)
	}

	var count int64
	if err := store.GORM().Table("agent_audit_events").Where("run_id = ?", "run_1").Count(&count).Error; err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 2 {
		t.Fatalf("audit event count = %d, want 2", count)
	}

	err = store.GORM().Exec(`UPDATE agent_audit_events SET status = 'tampered' WHERE id = ?`, started.ID).Error
	if err == nil {
		t.Fatal("updating an audit event unexpectedly succeeded")
	}
	err = store.GORM().Exec(`DELETE FROM agent_audit_events WHERE id = ?`, started.ID).Error
	if err == nil {
		t.Fatal("deleting an audit event unexpectedly succeeded")
	}
}

func TestAuditEventRepositoryRejectsInvalidEvents(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})

	recorder := NewAuditEventRepository(store.GORM())
	err = recorder.Record(context.Background(), nil)
	if err == nil {
		t.Fatal("Record(nil) unexpectedly succeeded")
	}

	event := audit.Event{ID: "invalid"}
	if _, err := audit.ValidateEvent(event); err == nil {
		t.Fatal("Validate() unexpectedly succeeded")
	}
	if err := recorder.Record(context.Background(), &event); err == nil {
		t.Fatal("Record(invalid) unexpectedly succeeded")
	}

	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("Record(invalid) error = %v, want domain error", err)
	}
}

func TestAuditEventRepositoryPreservesProvenance(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})

	recorder := NewAuditEventRepository(store.GORM())
	event := audit.Event{
		ID:                    "run_1:tool.completed",
		Type:                  audit.EventToolCallCompleted,
		OccurredAt:            time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		RootRunID:             "run_1",
		RunID:                 "run_1",
		ThreadID:              "thread_1",
		UserID:                "user_1",
		Protocol:              "ag_ui",
		TraceID:               "0123456789abcdef0123456789abcdef",
		SpanID:                "0123456789abcdef",
		AgentName:             "worker",
		AgentVersion:          "1.0.0",
		AgentDefinitionDigest: "sha256:agent",
		DefinitionName:        "deployment",
		DefinitionVersion:     "1.0.0",
		DefinitionDigest:      "sha256:deployment",
		ToolCallID:            "call_1",
		ToolName:              "test_tool",
		Model:                 "test-model",
		Provider:              "test",
		Status:                audit.StatusSucceeded,
		Payload:               map[string]any{"output_digest": "sha256:output"},
	}
	if err := recorder.Record(context.Background(), &event); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	var stored struct {
		Protocol              string
		TraceID               string
		SpanID                string
		AgentDefinitionDigest string
		PayloadJSON           string
	}
	if err := store.GORM().Table("agent_audit_events").
		Select("protocol", "trace_id", "span_id", "agent_definition_digest", "payload_json").
		Where("id = ?", event.ID).
		Scan(&stored).Error; err != nil {
		t.Fatalf("read audit event: %v", err)
	}
	if stored.Protocol != event.Protocol ||
		stored.TraceID != event.TraceID ||
		stored.SpanID != event.SpanID ||
		stored.AgentDefinitionDigest != event.AgentDefinitionDigest {
		t.Fatalf("stored provenance = %#v", stored)
	}
	if !strings.Contains(stored.PayloadJSON, "sha256:output") {
		t.Fatalf("stored payload = %q", stored.PayloadJSON)
	}
}

func TestAuditEventRepositoryRecoversInterruptedEvents(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})

	recorder := NewAuditEventRepository(store.GORM())
	ctx := context.Background()
	occurred := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	startedEvents := []audit.Event{
		{
			ID:   "run_1:run.started",
			Type: audit.EventRunStarted, OccurredAt: occurred,
			RootRunID: "run_1", RunID: "run_1", ThreadID: "thread_1",
			UserID: "user_1", Protocol: "ag_ui", AgentName: "coordinator",
			Status: audit.StatusRunning,
		},
		{
			ID:   "call_1:llm.call.started",
			Type: audit.EventLLMCallStarted, OccurredAt: occurred,
			RootRunID: "run_1", RunID: "run_1", ThreadID: "thread_1",
			UserID: "user_1", Protocol: "ag_ui", AgentName: "coordinator",
			Model: "test-model", Provider: "test", Status: audit.StatusRunning,
		},
		{
			ID:   "run_1/coordinator/call_1:tool.call.started",
			Type: audit.EventToolCallStarted, OccurredAt: occurred,
			RootRunID: "run_1", RunID: "run_1", ThreadID: "thread_1",
			UserID: "user_1", Protocol: "ag_ui", AgentName: "coordinator",
			ToolCallID: "call_1", ToolName: "test_tool", Status: audit.StatusRunning,
		},
		{
			ID:   "run_1/coordinator/worker:agent.delegation.started",
			Type: audit.EventDelegationStarted, OccurredAt: occurred,
			RootRunID: "run_1", RunID: "run_1", ThreadID: "thread_1",
			UserID: "user_1", Protocol: "ag_ui", CallerAgent: "coordinator",
			TargetAgent: "worker", Status: audit.StatusRunning,
		},
	}
	for index := range startedEvents {
		if err := recorder.Record(ctx, &startedEvents[index]); err != nil {
			t.Fatalf("Record(started %d) error = %v", index, err)
		}
	}

	recoveryTime := occurred.Add(5 * time.Second)
	if err := recorder.RecoverInterrupted(ctx, recoveryTime); err != nil {
		t.Fatalf("RecoverInterrupted() error = %v", err)
	}
	if err := recorder.RecoverInterrupted(ctx, recoveryTime.Add(time.Second)); err != nil {
		t.Fatalf("idempotent RecoverInterrupted() error = %v", err)
	}

	expected := map[string]string{
		"run_1:run.started":                                 "run_1:run.failed",
		"call_1:llm.call.started":                           "call_1:llm.call.failed",
		"run_1/coordinator/call_1:tool.call.started":        "run_1/coordinator/call_1:tool.call.failed",
		"run_1/coordinator/worker:agent.delegation.started": "run_1/coordinator/worker:agent.delegation.failed",
	}
	var count int64
	if err := store.GORM().Table("agent_audit_events").Count(&count).Error; err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != int64(len(startedEvents)*2) {
		t.Fatalf("event count after recovery = %d, want %d", count, len(startedEvents)*2)
	}
	for originalID, failedID := range expected {
		var stored struct {
			EventType    string
			OccurredAt   time.Time
			Status       string
			DurationMS   int64
			ErrorCode    string
			ErrorMessage string
			PayloadJSON  string
		}
		if err := store.GORM().Table("agent_audit_events").
			Select("event_type", "occurred_at", "status", "duration_ms", "error_code", "error_message", "payload_json").
			Where("id = ?", failedID).
			Scan(&stored).Error; err != nil {
			t.Fatalf("read recovered event %q: %v", failedID, err)
		}
		if stored.Status != string(audit.StatusFailed) ||
			stored.DurationMS != 5000 ||
			stored.ErrorCode != domain.CodeRuntimeInterrupted {
			t.Fatalf("recovered %q = %+v", failedID, stored)
		}
		if !strings.Contains(stored.PayloadJSON, `"recovery":"startup"`) ||
			!strings.Contains(stored.PayloadJSON, `"original_event_id":"`+originalID+`"`) {
			t.Fatalf("recovered %q payload = %q", failedID, stored.PayloadJSON)
		}
	}
}

func TestAuditEventRepositoryRecoveryRespectsExistingTerminalEvidence(t *testing.T) {
	lifecycles := []struct {
		started   audit.Event
		completed audit.Event
	}{
		{
			started: audit.Event{
				ID:   "run_1:run.started",
				Type: audit.EventRunStarted, OccurredAt: time.Now().UTC(),
				RootRunID: "run_1", RunID: "run_1", Status: audit.StatusRunning,
			},
		},
		{
			started: audit.Event{
				ID:   "call_1:llm.call.started",
				Type: audit.EventLLMCallStarted, OccurredAt: time.Now().UTC(),
				RootRunID: "run_1", RunID: "run_1", AgentName: "coordinator",
				Model: "test-model", Status: audit.StatusRunning,
			},
		},
		{
			started: audit.Event{
				ID:   "run_1/coordinator/call_1:tool.call.started",
				Type: audit.EventToolCallStarted, OccurredAt: time.Now().UTC(),
				RootRunID: "run_1", RunID: "run_1", ToolCallID: "call_1",
				ToolName: "test_tool", Status: audit.StatusRunning,
			},
		},
		{
			started: audit.Event{
				ID:   "run_1/coordinator/worker:agent.delegation.started",
				Type: audit.EventDelegationStarted, OccurredAt: time.Now().UTC(),
				RootRunID: "run_1", RunID: "run_1", CallerAgent: "coordinator",
				TargetAgent: "worker", Status: audit.StatusRunning,
			},
		},
	}

	for _, lifecycle := range lifecycles {
		t.Run(string(lifecycle.started.Type), func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close sqlite: %v", err)
				}
			})
			recorder := NewAuditEventRepository(store.GORM())
			ctx := context.Background()
			if err := recorder.Record(ctx, &lifecycle.started); err != nil {
				t.Fatalf("Record(started) error = %v", err)
			}

			completed := lifecycle.started
			base := strings.TrimSuffix(completed.ID, ":"+string(completed.Type))
			completedType := strings.Replace(string(completed.Type), ".started", ".completed", 1)
			completed.ID = base + ":" + completedType
			completed.Type = audit.EventType(completedType)
			completed.OccurredAt = completed.OccurredAt.Add(time.Second)
			completed.Status = audit.StatusSucceeded
			completed.DurationMS = 1000
			if err := recorder.Record(ctx, &completed); err != nil {
				t.Fatalf("Record(completed) error = %v", err)
			}

			if err := recorder.RecoverInterrupted(ctx, completed.OccurredAt.Add(time.Second)); err != nil {
				t.Fatalf("RecoverInterrupted() error = %v", err)
			}
			var failed int64
			if err := store.GORM().Table("agent_audit_events").
				Where("status = ?", string(audit.StatusFailed)).
				Count(&failed).Error; err != nil {
				t.Fatalf("count failed events: %v", err)
			}
			if failed != 0 {
				t.Fatalf("recovery invented %d terminal failures", failed)
			}
		})
	}
}

func TestAuditEventRepositoryRecoveryFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		eventID    string
		recoverAt  func(time.Time) time.Time
		wantErrStr string
	}{
		{
			name:       "malformed lifecycle ID",
			eventID:    "legacy-started",
			recoverAt:  func(occurred time.Time) time.Time { return occurred.Add(time.Second) },
			wantErrStr: "stable lifecycle ID rule",
		},
		{
			name:       "clock moved backwards",
			eventID:    "run_1:run.started",
			recoverAt:  func(occurred time.Time) time.Time { return occurred.Add(-time.Second) },
			wantErrStr: "recovery timestamp precedes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close sqlite: %v", err)
				}
			})
			occurred := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
			event := audit.Event{
				ID:   test.eventID,
				Type: audit.EventRunStarted, OccurredAt: occurred,
				RootRunID: "run_1", RunID: "run_1", Status: audit.StatusRunning,
			}
			recorder := NewAuditEventRepository(store.GORM())
			if err := recorder.Record(context.Background(), &event); err != nil {
				t.Fatalf("Record(started) error = %v", err)
			}

			err = recorder.RecoverInterrupted(context.Background(), test.recoverAt(occurred))
			if err == nil || !strings.Contains(err.Error(), test.wantErrStr) {
				t.Fatalf("RecoverInterrupted() error = %v, want %q", err, test.wantErrStr)
			}
			var count int64
			if err := store.GORM().Table("agent_audit_events").Count(&count).Error; err != nil {
				t.Fatalf("count audit events: %v", err)
			}
			if count != 1 {
				t.Fatalf("event count after failed recovery = %d, want unchanged 1", count)
			}
		})
	}
}

func TestAuditEventRepositoryRecoverySkipsEventsOutsideWindow(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	recorder := NewAuditEventRepository(store.GORM())
	ctx := context.Background()

	ancient := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	ancientOrphan := audit.Event{
		ID:   "run_ancient:llm.call.started",
		Type: audit.EventLLMCallStarted, OccurredAt: ancient,
		RootRunID: "run_ancient", RunID: "run_ancient", ThreadID: "thread_1",
		UserID: "user_1", AgentName: "coordinator",
		Model: "test-model", Provider: "test", Status: audit.StatusRunning,
	}
	if err := recorder.Record(ctx, &ancientOrphan); err != nil {
		t.Fatalf("Record(ancient) error = %v", err)
	}

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := recorder.RecoverInterrupted(ctx, now); err != nil {
		t.Fatalf("RecoverInterrupted() error = %v", err)
	}
	var recovered int64
	if err := store.GORM().Table("agent_audit_events").
		Where("id = ?", "run_ancient:llm.call.failed").
		Count(&recovered).Error; err != nil {
		t.Fatalf("count recovered: %v", err)
	}
	if recovered != 0 {
		t.Fatal("recovery closed an orphan far outside the recovery window")
	}
}

func TestAuditEventIndexSetMatchesWritePathContract(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	var indexes []string
	if err := store.GORM().
		Raw("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'agent_audit_events' AND name NOT LIKE 'sqlite_autoindex%'").
		Scan(&indexes).Error; err != nil {
		t.Fatalf("read audit indexes: %v", err)
	}
	// run_id serves idempotency probes, type+occurred serves the recovery
	// scan, trace_id serves incident correlation. Everything else was dropped
	// to keep the insert path at three secondary B-trees; aggregate questions
	// are answered by Prometheus metrics instead.
	want := []string{
		"idx_agent_audit_events_run_id",
		"idx_agent_audit_events_trace_id",
		"idx_agent_audit_events_type_occurred",
	}
	slices.Sort(indexes)
	if !slices.Equal(indexes, want) {
		t.Fatalf("audit indexes = %v, want %v", indexes, want)
	}
}
