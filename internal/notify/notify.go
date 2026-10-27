// Package notify delivers terminal run outcomes to the control plane
// (runtime -> liki-web) as Standard Webhooks-signed CloudEvents. Delivery is
// asynchronous with bounded retries; a failed delivery is logged, never
// surfaced into the run outcome.
package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/ml8s/liki-agent-runtime/internal/execution"
)

const (
	eventType       = "hk.liki.run.completed"
	queueSize       = 128
	defaultTimeout  = 5 * time.Second
	defaultAttempts = 3
	defaultBackoff  = time.Second
)

// Record is one terminal run outcome to deliver.
type Record struct {
	RunID       string
	ThreadID    string
	UserID      string
	UserMessage string
	Status      string // completed | failed | canceled (A2A TaskState vocabulary)
	Answer      string
	Error       string
	Usage       execution.RunUsage
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Config wires the completion notifier.
type Config struct {
	URL      string
	Secret   string // Standard Webhooks secret (whsec_...)
	Source   string // runtime identity, e.g. spiffe://liki/runtime/natal
	Profile  string // deployed expert
	Timeout  time.Duration
	Attempts int
	Backoff  time.Duration
	Logger   *slog.Logger
	Now      func() time.Time
	NewID    func() string
	Client   *http.Client
}

// Notifier delivers run-completed callbacks asynchronously.
type Notifier struct {
	cfg     Config
	webhook *standardwebhooks.Webhook
	client  *http.Client
	queue   chan Record
	wg      sync.WaitGroup
	closed  atomic.Bool
}

// New validates the configuration and starts the delivery worker.
func New(cfg Config) (*Notifier, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("callback URL is required")
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		return nil, errors.New("callback secret is required")
	}
	if strings.TrimSpace(cfg.Source) == "" {
		return nil, errors.New("callback source is required")
	}
	if strings.TrimSpace(cfg.Profile) == "" {
		return nil, errors.New("callback profile is required")
	}
	webhook, err := standardwebhooks.NewWebhook(cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("callback secret: %w", err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = defaultAttempts
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = defaultBackoff
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = newEventID
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	notifier := &Notifier{
		cfg:     cfg,
		webhook: webhook,
		client:  client,
		queue:   make(chan Record, queueSize),
	}
	notifier.wg.Add(1)
	go notifier.run()
	return notifier, nil
}

// Notify enqueues a record without blocking the caller. When the queue is full
// the record is dropped with an error log (completion callbacks are best
// effort on the runtime side; liki-web owns reconciliation).
func (n *Notifier) Notify(record Record) {
	if n.closed.Load() {
		n.cfg.Logger.Warn("run callback dropped: notifier closed", "run_id", record.RunID)
		return
	}
	select {
	case n.queue <- record:
	default:
		n.cfg.Logger.Error("run callback dropped: queue full", "run_id", record.RunID)
	}
}

// Close stops accepting records, drains the queue, and waits for the worker.
func (n *Notifier) Close(ctx context.Context) error {
	if n.closed.Swap(true) {
		return nil
	}
	close(n.queue)
	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *Notifier) run() {
	defer n.wg.Done()
	for record := range n.queue {
		n.deliver(record)
	}
}

func (n *Notifier) deliver(record Record) {
	payload, eventID, err := n.payload(record)
	if err != nil {
		n.cfg.Logger.Error("run callback payload failed", "run_id", record.RunID, "error", err)
		return
	}
	var lastErr error
	for attempt := 1; attempt <= n.cfg.Attempts; attempt++ {
		lastErr = n.post(eventID, record.RunID, payload)
		if lastErr == nil {
			return
		}
		if attempt < n.cfg.Attempts {
			time.Sleep(n.cfg.Backoff * time.Duration(1<<(attempt-1)))
		}
	}
	n.cfg.Logger.Error("run callback delivery failed",
		"run_id", record.RunID, "attempts", n.cfg.Attempts, "error", lastErr)
}

func (n *Notifier) post(eventID, runID string, payload []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), n.cfg.Timeout)
	defer cancel()
	now := n.cfg.Now()
	signature, err := n.webhook.Sign(eventID, now, payload)
	if err != nil {
		return fmt.Errorf("sign callback: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build callback request: %w", err)
	}
	// CloudEvents structured content mode: the media type is part of the spec
	// (CE 1.0 §3.1), not a free choice.
	request.Header.Set("Content-Type", "application/cloudevents+json")
	request.Header.Set(standardwebhooks.HeaderWebhookID, eventID)
	request.Header.Set(standardwebhooks.HeaderWebhookTimestamp, strconv.FormatInt(now.Unix(), 10))
	request.Header.Set(standardwebhooks.HeaderWebhookSignature, signature)
	request.Header.Set("Idempotency-Key", runID)
	response, err := n.client.Do(request)
	if err != nil {
		return fmt.Errorf("deliver callback: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("callback returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (n *Notifier) payload(record Record) ([]byte, string, error) {
	eventID := n.cfg.NewID()
	data := map[string]any{
		"run_id":       record.RunID,
		"user_id":      record.UserID,
		"thread_id":    record.ThreadID,
		"profile":      n.cfg.Profile,
		"status":       record.Status,
		"user_message": record.UserMessage,
		"usage": map[string]int{
			"llm_calls":         record.Usage.LLMCalls,
			"prompt_tokens":     record.Usage.PromptTokens,
			"completion_tokens": record.Usage.CompletionTokens,
			"thought_tokens":    record.Usage.ThoughtTokens,
		},
		"started_at":  record.StartedAt.UTC().Format(time.RFC3339),
		"finished_at": record.FinishedAt.UTC().Format(time.RFC3339),
	}
	if record.Answer != "" {
		data["answer"] = record.Answer
	}
	if record.Error != "" {
		data["error"] = record.Error
	}
	envelope := map[string]any{
		"specversion":     "1.0",
		"id":              eventID,
		"source":          n.cfg.Source,
		"type":            eventType,
		"time":            n.cfg.Now().UTC().Format(time.RFC3339),
		"datacontenttype": "application/json",
		"data":            data,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", fmt.Errorf("encode callback payload: %w", err)
	}
	return raw, eventID, nil
}

func newEventID() string {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(buffer[:])
}
