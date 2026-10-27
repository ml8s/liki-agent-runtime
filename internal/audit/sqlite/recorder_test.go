package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
)

var (
	_ audit.Recorder              = (*AuditRecorder)(nil)
	_ audit.RunExistenceChecker   = (*AuditRecorder)(nil)
	_ audit.InterruptionRecoverer = (*AuditRecorder)(nil)
)

func newRecorderStore(t *testing.T) (*DB, *AuditRecorder) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	recorder, err := NewAuditRecorder(store)
	if err != nil {
		t.Fatalf("NewAuditRecorder() error = %v", err)
	}
	t.Cleanup(func() {
		if err := recorder.Close(); err != nil {
			t.Errorf("close recorder: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	return store, recorder
}

func recorderEvent(runID string, terminal bool) audit.Event {
	occurred := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	eventType := audit.EventLLMCallStarted
	status := audit.StatusRunning
	if terminal {
		eventType = audit.EventLLMCallCompleted
		status = audit.StatusSucceeded
	}
	return audit.Event{
		ID:         runID + ":" + string(eventType),
		Type:       eventType,
		OccurredAt: occurred,
		RootRunID:  domain.ID(runID),
		RunID:      domain.ID(runID),
		ThreadID:   "thread_recorder",
		UserID:     "user_1",
		AgentName:  "coordinator",
		Model:      "test-model",
		Provider:   "test",
		Status:     status,
	}
}

func TestAuditRecorderAppendsDurablyInEnqueueOrder(t *testing.T) {
	store, recorder := newRecorderStore(t)
	ctx := context.Background()
	for index := 0; index < 20; index++ {
		runID := fmt.Sprintf("run_order_%02d", index)
		started := recorderEvent(runID, false)
		if err := recorder.Record(ctx, &started); err != nil {
			t.Fatalf("Record(started %d) error = %v", index, err)
		}
		completed := recorderEvent(runID, true)
		if err := recorder.Record(ctx, &completed); err != nil {
			t.Fatalf("Record(completed %d) error = %v", index, err)
		}
	}
	var ids []string
	if err := store.GORM().
		Table("agent_audit_events").
		Order("rowid ASC").
		Pluck("id", &ids).Error; err != nil {
		t.Fatalf("read audit order: %v", err)
	}
	if len(ids) != 40 {
		t.Fatalf("durable event count = %d, want 40", len(ids))
	}
	for index := 0; index < 40; index++ {
		runID := fmt.Sprintf("run_order_%02d", index/2)
		suffix := "llm.call.started"
		if index%2 == 1 {
			suffix = "llm.call.completed"
		}
		if want := runID + ":" + suffix; ids[index] != want {
			t.Fatalf("event %d = %q, want %q", index, ids[index], want)
		}
	}
}

func TestAuditRecorderKeepsConflictIsolatedInsideOneBatch(t *testing.T) {
	_, recorder := newRecorderStore(t)
	ctx := context.Background()

	// Commit a terminal event whose ID a later job will conflict with.
	preexisting := recorderEvent("run_conflict", true)
	if err := recorder.Record(ctx, &preexisting); err != nil {
		t.Fatalf("Record(preexisting) error = %v", err)
	}

	// Build one deterministic batch: valid, conflicting, valid.
	validFirst := recorderEvent("run_batch_a", false)
	conflicting := preexisting
	conflicting.Type = audit.EventLLMCallFailed
	conflicting.Status = audit.StatusFailed
	conflicting.ErrorCode = domain.CodeLLMCallFailed
	validLast := recorderEvent("run_batch_b", false)
	batch := []recorderJob{
		{event: &validFirst, result: make(chan recorderResult, 1)},
		{event: &conflicting, result: make(chan recorderResult, 1)},
		{event: &validLast, result: make(chan recorderResult, 1)},
	}
	if !recorder.execute(batch) {
		t.Fatal("execute(batch) reported a writer failure")
	}
	if result := <-batch[0].result; result.err != nil {
		t.Fatalf("first valid job error = %v", result.err)
	}
	conflictResult := <-batch[1].result
	var conflictErr *domain.Error
	if !errors.As(conflictResult.err, &conflictErr) || conflictErr.Code != audit.CodeAuditEventConflict {
		t.Fatalf("conflicting job error = %v, want %s", conflictResult.err, audit.CodeAuditEventConflict)
	}
	if conflictResult.status != audit.AppendStatusConflict {
		t.Fatalf("conflicting job status = %q, want %q", conflictResult.status, audit.AppendStatusConflict)
	}
	if result := <-batch[2].result; result.err != nil {
		t.Fatalf("last valid job error = %v", result.err)
	}
	for _, runID := range []string{"run_batch_a", "run_batch_b"} {
		exists, err := recorder.RunExists(ctx, domain.ID(runID))
		if err != nil {
			t.Fatalf("RunExists(%s) error = %v", runID, err)
		}
		if !exists {
			t.Fatalf("run %s missing after batch commit", runID)
		}
	}
}

func TestAuditRecorderIdempotentRetry(t *testing.T) {
	_, recorder := newRecorderStore(t)
	ctx := context.Background()
	event := recorderEvent("run_idempotent", false)
	for attempt := 0; attempt < 2; attempt++ {
		if err := recorder.Record(ctx, &event); err != nil {
			t.Fatalf("Record(attempt %d) error = %v", attempt, err)
		}
	}
	var count int64
	if err := recorder.repository.db.
		Table("agent_audit_events").
		Where("run_id = ?", "run_idempotent").
		Count(&count).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("event count = %d, want 1", count)
	}
}

type recordingWriterMetrics struct {
	statuses []string
	batches  []int
	depths   []int
}

func (m *recordingWriterMetrics) ObserveAuditAppend(status string, _ time.Duration) {
	m.statuses = append(m.statuses, status)
}

func (m *recordingWriterMetrics) ObserveAuditAppendBatch(eventsPerCommit int) {
	m.batches = append(m.batches, eventsPerCommit)
}

func (m *recordingWriterMetrics) ObserveAuditQueueDepth(depth int) {
	m.depths = append(m.depths, depth)
}

func TestAuditRecorderObservesBatchAndQueueMetrics(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	metrics := &recordingWriterMetrics{}
	recorder, err := NewAuditRecorder(store, WithRecorderMetrics(metrics))
	if err != nil {
		t.Fatalf("NewAuditRecorder() error = %v", err)
	}
	t.Cleanup(func() {
		if err := recorder.Close(); err != nil {
			t.Errorf("close recorder: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	ctx := context.Background()

	// One isolated append observes a single-event batch; a direct grouped
	// execution observes its own size. Queue depth is sampled per append.
	isolated := recorderEvent("run_metrics_isolated", false)
	if err := recorder.Record(ctx, &isolated); err != nil {
		t.Fatalf("Record(isolated) error = %v", err)
	}
	first := recorderEvent("run_metrics_group_a", false)
	second := recorderEvent("run_metrics_group_b", false)
	batch := []recorderJob{
		{event: &first, result: make(chan recorderResult, 1)},
		{event: &second, result: make(chan recorderResult, 1)},
	}
	if !recorder.execute(batch) {
		t.Fatal("execute(batch) reported a writer failure")
	}

	if len(metrics.batches) != 2 || metrics.batches[0] != 1 || metrics.batches[1] != 2 {
		t.Fatalf("observed batches = %v, want [1 2]", metrics.batches)
	}
	if len(metrics.depths) != 1 {
		t.Fatalf("queue depth samples = %v, want one from Record", metrics.depths)
	}
}

func TestAuditRecorderCloseDrainsThenRejects(t *testing.T) {
	store, recorder := newRecorderStore(t)
	ctx := context.Background()

	const writers = 16
	const eventsPerWriter = 24
	var wait sync.WaitGroup
	started := make(chan struct{})
	var successCount int
	var mu sync.Mutex
	for writer := 0; writer < writers; writer++ {
		wait.Add(1)
		go func(writer int) {
			defer wait.Done()
			<-started
			for index := 0; index < eventsPerWriter; index++ {
				runID := fmt.Sprintf("run_drain_%02d_%02d", writer, index)
				event := recorderEvent(runID, false)
				if err := recorder.Record(ctx, &event); err == nil {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}
		}(writer)
	}
	close(started)
	time.Sleep(2 * time.Millisecond)
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	wait.Wait()

	// Every acknowledged event is durable; the closing race only decides how
	// many calls arrive before the closed flag.
	var durable int64
	if err := store.GORM().
		Table("agent_audit_events").
		Where("run_id LIKE ?", "run_drain_%").
		Count(&durable).Error; err != nil {
		t.Fatalf("count durable events: %v", err)
	}
	mu.Lock()
	acknowledged := successCount
	mu.Unlock()
	if durable != int64(acknowledged) {
		t.Fatalf("durable events = %d, acknowledged = %d", durable, acknowledged)
	}

	afterClose := recorderEvent("run_after_close", false)
	err := recorder.Record(ctx, &afterClose)
	var closedErr *domain.Error
	if !errors.As(err, &closedErr) || closedErr.Code != audit.CodeAuditAppendFailed {
		t.Fatalf("Record(after close) error = %v, want %s", err, audit.CodeAuditAppendFailed)
	}
}

func TestAuditRecorderQueueFullFailsClosed(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "agent-audit.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	// A writer-less recorder with capacity one keeps the test deterministic:
	// the queued job occupies the buffer and the next enqueue must fail.
	recorder := &AuditRecorder{
		repository: NewAuditEventRepository(store.GORM()),
		queue:      make(chan recorderJob, 1),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	event := recorderEvent("run_queue_full", false)
	first := recorderJob{event: &event, result: make(chan recorderResult, 1)}
	if err := recorder.enqueue(first); err != nil {
		t.Fatalf("enqueue(first) error = %v", err)
	}
	if err := recorder.enqueue(first); err == nil {
		t.Fatal("enqueue(second) unexpectedly succeeded")
	}
}

func TestAuditRecorderBatchInsertMatchesDirectPath(t *testing.T) {
	store, recorder := newRecorderStore(t)
	ctx := context.Background()

	// The same fully-populated event shape is written once through the direct
	// repository Create and once through the grouped multi-row statement.
	directEvent := recorderEvent("run_roundtrip_direct", true)
	directEvent.ParentRunID = "run_parent"
	directEvent.Protocol = "ag_ui"
	directEvent.TraceID = "0123456789abcdef0123456789abcdef"
	directEvent.SpanID = "0123456789abcdef"
	directEvent.DefinitionName = "definition"
	directEvent.DefinitionVersion = "1.2.3"
	directEvent.DefinitionDigest = "sha256:direct"
	directEvent.AgentVersion = "2.0.0"
	directEvent.AgentDefinitionDigest = "sha256:agent"
	directEvent.CallerAgent = "caller"
	directEvent.TargetAgent = "target"
	directEvent.DelegationDepth = 2
	directEvent.ToolCallID = "call_1"
	directEvent.ToolName = "tool"
	directEvent.DurationMS = 1234
	directEvent.Payload = map[string]any{"key": "value", "count": 2}
	if err := recorder.repository.Record(ctx, &directEvent); err != nil {
		t.Fatalf("direct Record() error = %v", err)
	}

	batchEvent := directEvent
	batchEvent.ID = "run_roundtrip_batch:llm.call.completed"
	batchEvent.RunID = "run_roundtrip_batch"
	batchEvent.RootRunID = "run_roundtrip_batch"
	batch := []recorderJob{
		{event: &batchEvent, result: make(chan recorderResult, 1)},
	}
	// Pad with a second event so the group takes the multi-row statement path.
	padding := recorderEvent("run_roundtrip_padding", false)
	batch = append(batch, recorderJob{event: &padding, result: make(chan recorderResult, 1)})
	if !recorder.execute(batch) {
		t.Fatal("execute(batch) reported a writer failure")
	}
	for index, job := range batch {
		if result := <-job.result; result.err != nil {
			t.Fatalf("batch job %d error = %v", index, result.err)
		}
	}

	// Compare every persisted column between the two paths.
	var rows []map[string]any
	if err := store.GORM().
		Table("agent_audit_events").
		Where("id IN ?", []string{directEvent.ID, batchEvent.ID}).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		t.Fatalf("read round-trip rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("round-trip row count = %d, want 2", len(rows))
	}
	// Identity columns legitimately differ between the two probe rows.
	for _, column := range []string{"id", "run_id", "root_run_id"} {
		delete(rows[0], column)
		delete(rows[1], column)
	}
	for column, directValue := range rows[0] {
		if batchValue, ok := rows[1][column]; !ok || fmt.Sprint(directValue) != fmt.Sprint(batchValue) {
			t.Fatalf("column %q differs: direct=%v batch=%v", column, directValue, batchValue)
		}
	}
	if len(rows[0]) != len(rows[1]) {
		t.Fatalf("column count differs: direct=%d batch=%d", len(rows[0]), len(rows[1]))
	}
}

func TestAuditStoreCheckHealth(t *testing.T) {
	store, recorder := newRecorderStore(t)
	ctx := context.Background()
	if err := recorder.Record(ctx, ptrEvent(recorderEvent("run_health", false))); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	health := store.CheckHealth(ctx)
	if !health.OK {
		t.Fatalf("CheckHealth() = %+v, want OK", health)
	}
	if health.Name != "sqlite" {
		t.Fatalf("CheckHealth().Name = %q, want sqlite", health.Name)
	}

	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close() error = %v", err)
	}
	health = store.CheckHealth(ctx)
	if health.OK {
		t.Fatal("CheckHealth() after close = OK, want failure")
	}
}

func TestAuditRecorderRecordAfterCloseFails(t *testing.T) {
	store, recorder := newRecorderStore(t)
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	event := recorderEvent("run_after_close", false)
	if err := recorder.Record(context.Background(), &event); err == nil {
		t.Fatal("Record() after Close() accepted an event")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close() error = %v", err)
	}
}

func TestAuditRecorderDoubleCloseIsNoop(t *testing.T) {
	store, recorder := newRecorderStore(t)
	if err := recorder.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close() error = %v", err)
	}
}

func TestAuditStoreInMemoryMode(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// In-memory mode shares one handle between read and write paths.
	if store.writeGORM() != store.GORM() {
		t.Fatal("in-memory writeGORM() != GORM(); want shared handle")
	}

	recorder, err := NewAuditRecorder(store)
	if err != nil {
		t.Fatalf("NewAuditRecorder() error = %v", err)
	}
	ctx := context.Background()
	started := recorderEvent("run_memory", false)
	if err := recorder.Record(ctx, &started); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	completed := recorderEvent("run_memory", true)
	if err := recorder.Record(ctx, &completed); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var count int64
	if err := store.GORM().Table("agent_audit_events").Count(&count).Error; err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if count != 2 {
		t.Fatalf("in-memory event count = %d, want 2", count)
	}
}

func ptrEvent(event audit.Event) *audit.Event {
	return &event
}

func TestAuditStorageStats(t *testing.T) {
	store, recorder := newRecorderStore(t)
	ctx := context.Background()
	started := recorderEvent("stats-run", false)
	completed := recorderEvent("stats-run", true)
	if err := recorder.Record(ctx, &started); err != nil {
		t.Fatalf("Record(started) error = %v", err)
	}
	if err := recorder.Record(ctx, &completed); err != nil {
		t.Fatalf("Record(completed) error = %v", err)
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.Events != 2 {
		t.Fatalf("Stats() events = %d, want 2", stats.Events)
	}
}
