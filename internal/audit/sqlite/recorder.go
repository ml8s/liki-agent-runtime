package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"gorm.io/gorm"
)

const (
	// recorderQueueCapacity bounds buffered append requests. When the writer
	// falls further behind than this, Record fails honestly instead of
	// blocking callers indefinitely on a wedged writer.
	recorderQueueCapacity = 8192
	// recorderMaxBatch caps one commit transaction so a single burst cannot
	// hold the write connection for an unbounded interval.
	recorderMaxBatch = 64
)

// AuditRecorder is the production audit.Recorder. It preserves the synchronous
// durability contract of the repository — Record returns only after its event
// is committed by a synchronous=FULL transaction — while one dedicated writer
// serializes appends and group-commits concurrent events:
//
//   - total order: events become durable in FIFO enqueue order;
//   - durability: every event is acknowledged only after its commit fsyncs;
//   - grouping: a batch is written as one multi-row INSERT, which SQLite
//     commits as one implicit transaction with one fsync;
//   - isolation: a failed group statement falls back to per-event appends, so
//     a UNIQUE conflict still fails exactly its own event while its
//     neighbours commit;
//   - failure: a queue-full or closed recorder returns a coded error instead
//     of blocking, so callers keep their retry/reconciliation semantics;
//   - shutdown: Close drains every queued event before returning.
type AuditRecorder struct {
	repository *AuditEventRepository
	write      *gorm.DB
	queue      chan recorderJob

	// mu guards the closed flag together with enqueueing so a Record either
	// lands in the queue before Close drains it or fails with a closed error.
	mu     sync.RWMutex
	closed bool

	stop         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
	metrics      audit.AppendMetrics
	batchMetrics audit.AppendBatchMetrics
	queueMetrics audit.QueueDepthMetrics

	// Batch diagnostics: average events per grouped commit. Only the writer
	// goroutine mutates them; benchmarks read them after RunParallel returns.
	groupedCommits atomic.Uint64
	groupedEvents  atomic.Uint64
}

// RecorderOption customizes an AuditRecorder.
type RecorderOption func(*AuditRecorder)

// WithRecorderMetrics observes every append outcome and its enqueue-to-commit
// latency.
func WithRecorderMetrics(metrics audit.AppendMetrics) RecorderOption {
	return func(recorder *AuditRecorder) {
		recorder.metrics = metrics
	}
}

type recorderJob struct {
	// event is the append payload for insert jobs.
	event *audit.Event
	// run executes an arbitrary serialized write (startup recovery) on the
	// dedicated write handle.
	run func(db *gorm.DB) error
	// result receives exactly one outcome. It is buffered so the writer never
	// blocks on a caller, including while failing a batch defensively.
	result chan recorderResult
}

type recorderResult struct {
	status string
	err    error
}

// NewAuditRecorder builds the single-writer recorder over an opened store and
// starts its writer goroutine. Close must be called before store.Close.
func NewAuditRecorder(store *DB, options ...RecorderOption) (*AuditRecorder, error) {
	if store == nil {
		return nil, fmt.Errorf("audit store is required")
	}
	recorder := &AuditRecorder{
		repository: NewAuditEventRepository(store.GORM()),
		write:      store.writeGORM(),
		queue:      make(chan recorderJob, recorderQueueCapacity),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	for _, option := range options {
		option(recorder)
	}
	if batchMetrics, ok := recorder.metrics.(audit.AppendBatchMetrics); ok {
		recorder.batchMetrics = batchMetrics
	}
	if queueMetrics, ok := recorder.metrics.(audit.QueueDepthMetrics); ok {
		recorder.queueMetrics = queueMetrics
	}
	go recorder.runWriter()
	return recorder, nil
}

// Record appends one validated audit event and returns only after the event is
// durably committed. The wait deliberately ignores caller cancellation: audit
// evidence outlives request contexts, and terminal paths already run under
// context.WithoutCancel.
func (r *AuditRecorder) Record(_ context.Context, event *audit.Event) error {
	started := time.Now()
	status, err := r.record(event)
	r.observe(status, time.Since(started))
	return err
}

func (r *AuditRecorder) record(event *audit.Event) (string, error) {
	// Invalid input is rejected before the queue so error values stay identical
	// to the synchronous repository path.
	if event == nil {
		return audit.AppendStatusInvalid, audit.NewError(
			audit.CodeAuditAppendFailed,
			"audit event is required",
			domain.ErrInvalidInput,
		)
	}
	normalized, err := audit.ValidateEvent(*event)
	if err != nil {
		return audit.AppendStatusInvalid, err
	}
	*event = normalized
	job := recorderJob{event: event, result: make(chan recorderResult, 1)}
	if err := r.enqueue(job); err != nil {
		return audit.AppendStatusRejected, err
	}
	if r.queueMetrics != nil {
		r.queueMetrics.ObserveAuditQueueDepth(len(r.queue))
	}
	result := <-job.result
	return result.status, result.err
}

// enqueue hands one job to the writer. The check and the non-blocking send run
// under the read lock, and Close takes the write lock before draining, so a
// job can never be enqueued after the writer has exited.
func (r *AuditRecorder) enqueue(job recorderJob) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return audit.NewError(audit.CodeAuditAppendFailed, "audit recorder is closed", nil)
	}
	select {
	case r.queue <- job:
		return nil
	default:
		return audit.NewError(audit.CodeAuditAppendFailed, "audit writer queue is full", nil)
	}
}

// RunExists reports whether any audit evidence exists for the run id.
func (r *AuditRecorder) RunExists(ctx context.Context, runID domain.ID) (bool, error) {
	return r.repository.RunExists(ctx, runID)
}

// RecoverInterrupted closes interrupted lifecycles on the dedicated write
// connection, serialized with appends, inside the recovery transaction.
func (r *AuditRecorder) RecoverInterrupted(ctx context.Context, now time.Time) error {
	repository := r.repository
	job := recorderJob{
		run: func(db *gorm.DB) error {
			return repository.recoverInterrupted(ctx, db, now)
		},
		result: make(chan recorderResult, 1),
	}
	if err := r.enqueue(job); err != nil {
		return err
	}
	result := <-job.result
	return result.err
}

// Close stops accepting new appends, waits until every queued job has been
// executed, and returns. It is idempotent and must run before store.Close.
func (r *AuditRecorder) Close() error {
	r.closeOnce.Do(func() {
		r.markClosed()
		close(r.stop)
	})
	<-r.done
	return nil
}

// markClosed is the single transition into the closed state. Enqueue holds the
// read lock across its check-and-send, so once this write lock returns no new
// job can land in the queue behind the writer's back.
func (r *AuditRecorder) markClosed() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func (r *AuditRecorder) observe(status string, duration time.Duration) {
	if r.metrics != nil {
		r.metrics.ObserveAuditAppend(status, duration)
	}
}

func (r *AuditRecorder) observeBatch(events int) {
	if r.batchMetrics != nil {
		r.batchMetrics.ObserveAuditAppendBatch(events)
	}
}

func (r *AuditRecorder) runWriter() {
	defer close(r.done)
	for {
		select {
		case job := <-r.queue:
			if !r.process(job) {
				r.markClosed()
				r.failQueued("audit writer stopped after a failed batch")
				return
			}
		case <-r.stop:
			for {
				select {
				case job := <-r.queue:
					// Close already holds off new enqueues; drain what remains.
					if !r.process(job) {
						r.markClosed()
						r.failQueued("audit writer stopped during shutdown")
						return
					}
				default:
					return
				}
			}
		}
	}
}

// process executes one job together with whatever else is already queued. It
// returns false when the writer must stop; every affected job is failed first.
func (r *AuditRecorder) process(first recorderJob) (survived bool) {
	batch := make([]recorderJob, 0, recorderMaxBatch)
	batch = append(batch, first)
	batch = r.drain(batch)
	return r.execute(batch)
}

// drain moves queued jobs into the batch without blocking.
func (r *AuditRecorder) drain(batch []recorderJob) []recorderJob {
	for len(batch) < recorderMaxBatch {
		select {
		case job := <-r.queue:
			batch = append(batch, job)
		default:
			return batch
		}
	}
	return batch
}

// execute runs jobs strictly in FIFO order. Consecutive insert jobs share one
// grouped statement; a serialized closure job breaks the group and keeps its
// own transaction semantics.
func (r *AuditRecorder) execute(batch []recorderJob) bool {
	index := 0
	for index < len(batch) {
		if batch[index].run != nil {
			if !r.runClosure(batch[index]) {
				r.failJobs(batch[index+1:], "audit writer stopped after a failed job")
				return false
			}
			index++
			continue
		}
		end := index
		for end < len(batch) && batch[end].run == nil {
			end++
		}
		if !r.commitInserts(batch[index:end]) {
			r.failJobs(batch[end:], "audit writer stopped after a failed commit")
			return false
		}
		index = end
	}
	return true
}

func (r *AuditRecorder) runClosure(job recorderJob) (survived bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			survived = false
			job.result <- recorderResult{
				status: audit.AppendStatusError,
				err:    fmt.Errorf("audit writer job panicked: %v", recovered),
			}
		}
	}()
	err := job.run(r.write.WithContext(context.Background()))
	status := audit.AppendStatusOK
	if err != nil {
		status = audit.AppendStatusError
	}
	job.result <- recorderResult{status: status, err: err}
	return true
}

// commitInserts appends a run of events with one fsync for the whole group.
// The group runs as one multi-row INSERT statement: SQLite executes a single
// statement inside one implicit transaction, so the batch either fully commits
// or fully aborts. A failed statement (the rare conflict case) falls back to
// per-event appends that isolate each outcome exactly like the direct path.
func (r *AuditRecorder) commitInserts(batch []recorderJob) (survived bool) {
	// An isolated append runs in autocommit mode exactly like the synchronous
	// repository path: one statement, one fsync, no BEGIN/COMMIT round trips.
	// The grouped multi-row statement is reserved for sharing one fsync.
	if len(batch) == 1 {
		r.observeBatch(1)
		return r.commitSingleInsert(batch[0])
	}
	r.groupedCommits.Add(1)
	r.groupedEvents.Add(uint64(len(batch)))
	r.observeBatch(len(batch))
	outcomes := make([]recorderResult, len(batch))
	defer func() {
		if recovered := recover(); recovered != nil {
			survived = false
			r.sendOutcomes(batch, outcomes, fmt.Errorf("audit writer panicked: %v", recovered))
		}
	}()
	if err := r.execBatchInsert(batch); err == nil {
		for index := range outcomes {
			outcomes[index] = recorderResult{status: audit.AppendStatusOK, err: nil}
		}
	} else {
		for index, job := range batch {
			status, err := appendEvent(context.Background(), r.write, job.event)
			outcomes[index] = recorderResult{status: status, err: err}
		}
	}
	r.sendOutcomes(batch, outcomes, nil)
	return true
}

// execBatchInsert binds every event in the batch into one cached multi-row
// INSERT statement on the write connection. GORM's process-level statement
// cache keys by SQL text, so each distinct batch size is parsed once per
// process instead of once per transaction.
func (r *AuditRecorder) execBatchInsert(batch []recorderJob) error {
	fields := auditEventInsertPlanValue().fields
	placeholders := auditBatchPlaceholders(len(batch))
	args := make([]any, 0, len(batch)*len(fields))
	for _, job := range batch {
		model, err := newAuditEventModel(job.event)
		if err != nil {
			return err
		}
		value := reflect.ValueOf(model)
		for _, field := range fields {
			// ValueOf is GORM's own binding path, so future field kinds and
			// serializers behave exactly like the per-row Create path.
			bound, _ := field.ValueOf(context.Background(), value)
			args = append(args, bound)
		}
	}
	return r.write.WithContext(context.Background()).Exec(placeholders, args...).Error
}

func (r *AuditRecorder) commitSingleInsert(job recorderJob) (survived bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			survived = false
			job.result <- recorderResult{
				status: audit.AppendStatusError,
				err:    fmt.Errorf("audit writer panicked: %v", recovered),
			}
		}
	}()
	status, err := appendEvent(context.Background(), r.write, job.event)
	job.result <- recorderResult{status: status, err: err}
	return true
}

func (r *AuditRecorder) sendOutcomes(batch []recorderJob, outcomes []recorderResult, failure error) {
	for index, job := range batch {
		result := outcomes[index]
		if failure != nil {
			result = recorderResult{status: audit.AppendStatusError, err: failure}
		}
		job.result <- result
	}
}

func (r *AuditRecorder) failJobs(batch []recorderJob, cause string) {
	for _, job := range batch {
		job.result <- recorderResult{
			status: audit.AppendStatusError,
			err:    audit.NewError(audit.CodeAuditAppendFailed, cause, nil),
		}
	}
}

func (r *AuditRecorder) failQueued(cause string) {
	for {
		select {
		case job := <-r.queue:
			r.failJobs([]recorderJob{job}, cause)
		default:
			return
		}
	}
}
