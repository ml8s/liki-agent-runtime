package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

// benchmarkEvent builds a minimal valid lifecycle pair so benchmarks exercise
// the same validation and persistence path as production appends.
func benchmarkEvent(runID string, terminal bool) audit.Event {
	occurred := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
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
		ThreadID:   "thread_bench",
		UserID:     "user_bench",
		AgentName:  "coordinator",
		Model:      "bench-model",
		Provider:   "bench",
		Status:     status,
	}
}

func BenchmarkAuditEventRepositoryRecord(b *testing.B) {
	store, err := Open(filepath.Join(b.TempDir(), "agent-audit.db"))
	if err != nil {
		b.Fatalf("Open() error = %v", err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Fatalf("close sqlite: %v", err)
		}
	})
	recorder := NewAuditEventRepository(store.GORM())
	ctx := context.Background()
	// Benchmark rounds calibrate with a small b.N before the measured round;
	// globally unique run IDs keep every iteration on the real append path
	// instead of the idempotent-conflict path.
	var runSequence atomic.Int64

	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			runID := fmt.Sprintf("run_seq_%d", runSequence.Add(1))
			started := benchmarkEvent(runID, false)
			if err := recorder.Record(ctx, &started); err != nil {
				b.Fatalf("Record(started) error = %v", err)
			}
			completed := benchmarkEvent(runID, true)
			if err := recorder.Record(ctx, &completed); err != nil {
				b.Fatalf("Record(completed) error = %v", err)
			}
		}
	})

	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				runID := fmt.Sprintf("run_par_%d", runSequence.Add(1))
				started := benchmarkEvent(runID, false)
				if err := recorder.Record(ctx, &started); err != nil {
					b.Fatalf("Record(started) error = %v", err)
				}
				completed := benchmarkEvent(runID, true)
				if err := recorder.Record(ctx, &completed); err != nil {
					b.Fatalf("Record(completed) error = %v", err)
				}
			}
		})
	})
}

func BenchmarkAuditRecorderRecord(b *testing.B) {
	store, err := Open(filepath.Join(b.TempDir(), "agent-audit.db"))
	if err != nil {
		b.Fatalf("Open() error = %v", err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Fatalf("close sqlite: %v", err)
		}
	})
	recorder, err := NewAuditRecorder(store)
	if err != nil {
		b.Fatalf("NewAuditRecorder() error = %v", err)
	}
	b.Cleanup(func() {
		if err := recorder.Close(); err != nil {
			b.Fatalf("close recorder: %v", err)
		}
	})
	ctx := context.Background()
	var runSequence atomic.Int64

	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			runID := fmt.Sprintf("run_rec_seq_%d", runSequence.Add(1))
			started := benchmarkEvent(runID, false)
			if err := recorder.Record(ctx, &started); err != nil {
				b.Fatalf("Record(started) error = %v", err)
			}
			completed := benchmarkEvent(runID, true)
			if err := recorder.Record(ctx, &completed); err != nil {
				b.Fatalf("Record(completed) error = %v", err)
			}
		}
	})

	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		commitsBefore := recorder.groupedCommits.Load()
		eventsBefore := recorder.groupedEvents.Load()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				runID := fmt.Sprintf("run_rec_par_%d", runSequence.Add(1))
				started := benchmarkEvent(runID, false)
				if err := recorder.Record(ctx, &started); err != nil {
					b.Fatalf("Record(started) error = %v", err)
				}
				completed := benchmarkEvent(runID, true)
				if err := recorder.Record(ctx, &completed); err != nil {
					b.Fatalf("Record(completed) error = %v", err)
				}
			}
		})
		commits := recorder.groupedCommits.Load() - commitsBefore
		events := recorder.groupedEvents.Load() - eventsBefore
		if commits > 0 {
			b.ReportMetric(float64(events)/float64(commits), "events/commit")
		}
	})
}
