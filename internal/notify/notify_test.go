package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/ml8s/liki-agent-runtime/internal/execution"
	"github.com/ml8s/liki-agent-runtime/internal/notify"
)

const testSecret = "whsec_MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func testRecord() notify.Record {
	return notify.Record{
		RunID:       "run_1",
		ThreadID:    "thread_1",
		UserID:      "user_1",
		UserMessage: "你好",
		Status:      "completed",
		Answer:      "答案",
		Usage:       execution.RunUsage{LLMCalls: 2, PromptTokens: 10, CompletionTokens: 20, ThoughtTokens: 1},
		StartedAt:   time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC),
		FinishedAt:  time.Date(2026, 10, 19, 1, 1, 0, 0, time.UTC),
	}
}

func newTestNotifier(t *testing.T, url string) *notify.Notifier {
	t.Helper()
	notifier, err := notify.New(notify.Config{
		URL:      url,
		Secret:   testSecret,
		Source:   "spiffe://liki/runtime/natal",
		Profile:  "natal",
		Attempts: 3,
		Backoff:  time.Millisecond,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return notifier
}

type captured struct {
	headers http.Header
	body    []byte
}

type captureServer struct {
	mu       sync.Mutex
	requests []captured
}

func (c *captureServer) snapshot() []captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]captured(nil), c.requests...)
}

func newCaptureServer(t *testing.T, status func(attempt int32) int) (*httptest.Server, *captureServer, *atomic.Int32) {
	t.Helper()
	capture := &captureServer{}
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		body, _ := io.ReadAll(r.Body)
		capture.mu.Lock()
		capture.requests = append(capture.requests, captured{headers: r.Header.Clone(), body: body})
		capture.mu.Unlock()
		w.WriteHeader(status(attempt))
	}))
	t.Cleanup(server.Close)
	return server, capture, &attempts
}

func closeNotifier(t *testing.T, notifier *notify.Notifier) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notifier.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNotifierDeliversSignedCloudEvent(t *testing.T) {
	server, capture, _ := newCaptureServer(t, func(int32) int { return http.StatusOK })
	notifier := newTestNotifier(t, server.URL)

	notifier.Notify(testRecord())
	closeNotifier(t, notifier)

	requests := capture.snapshot()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	request := requests[0]

	// 用规范参考实现交叉验证签名。
	webhook, err := standardwebhooks.NewWebhook(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := webhook.Verify(request.body, request.headers); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}
	if got := request.headers.Get("Idempotency-Key"); got != "run_1" {
		t.Fatalf("Idempotency-Key = %q, want run_1", got)
	}
	if got := request.headers.Get("Content-Type"); got != "application/cloudevents+json" {
		t.Fatalf("Content-Type = %q, want application/cloudevents+json (CloudEvents structured mode)", got)
	}

	var envelope struct {
		SpecVersion string `json:"specversion"`
		ID          string `json:"id"`
		Source      string `json:"source"`
		Type        string `json:"type"`
		Time        string `json:"time"`
		Data        struct {
			RunID       string         `json:"run_id"`
			UserID      string         `json:"user_id"`
			ThreadID    string         `json:"thread_id"`
			Profile     string         `json:"profile"`
			Status      string         `json:"status"`
			UserMessage string         `json:"user_message"`
			Answer      string         `json:"answer"`
			Usage       map[string]int `json:"usage"`
			StartedAt   string         `json:"started_at"`
			FinishedAt  string         `json:"finished_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(request.body, &envelope); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if envelope.SpecVersion != "1.0" || envelope.Type != "hk.liki.run.completed" ||
		envelope.Source != "spiffe://liki/runtime/natal" || envelope.ID == "" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if _, err := time.Parse(time.RFC3339, envelope.Time); err != nil {
		t.Fatalf("envelope time = %q", envelope.Time)
	}
	data := envelope.Data
	if data.RunID != "run_1" || data.UserID != "user_1" || data.ThreadID != "thread_1" ||
		data.Profile != "natal" || data.Status != "completed" ||
		data.UserMessage != "你好" || data.Answer != "答案" {
		t.Fatalf("data = %+v", data)
	}
	if data.Usage["llm_calls"] != 2 || data.Usage["prompt_tokens"] != 10 ||
		data.Usage["completion_tokens"] != 20 || data.Usage["thought_tokens"] != 1 {
		t.Fatalf("usage = %v", data.Usage)
	}
	if _, err := time.Parse(time.RFC3339, data.StartedAt); err != nil {
		t.Fatalf("started_at = %q", data.StartedAt)
	}
	if _, err := time.Parse(time.RFC3339, data.FinishedAt); err != nil {
		t.Fatalf("finished_at = %q", data.FinishedAt)
	}
}

func TestNotifierRetriesThenSucceeds(t *testing.T) {
	server, capture, attempts := newCaptureServer(t, func(attempt int32) int {
		if attempt == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	notifier := newTestNotifier(t, server.URL)

	notifier.Notify(testRecord())
	closeNotifier(t, notifier)

	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry)", got)
	}
	if len(capture.snapshot()) != 2 {
		t.Fatalf("captured = %d, want 2", len(capture.snapshot()))
	}
}

func TestNotifierGivesUpAfterConfiguredAttempts(t *testing.T) {
	server, capture, attempts := newCaptureServer(t, func(int32) int { return http.StatusInternalServerError })
	notifier := newTestNotifier(t, server.URL)

	notifier.Notify(testRecord())
	closeNotifier(t, notifier)

	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if len(capture.snapshot()) != 3 {
		t.Fatalf("captured = %d, want 3", len(capture.snapshot()))
	}
}

func TestNotifierCloseDrainsQueue(t *testing.T) {
	server, capture, _ := newCaptureServer(t, func(int32) int { return http.StatusOK })
	notifier := newTestNotifier(t, server.URL)

	for index := 0; index < 3; index++ {
		record := testRecord()
		record.RunID = "run_" + string(rune('a'+index))
		notifier.Notify(record)
	}
	closeNotifier(t, notifier)

	if len(capture.snapshot()) != 3 {
		t.Fatalf("captured = %d, want 3 after drain", len(capture.snapshot()))
	}
}

func TestNotifierIgnoresNotifyAfterClose(t *testing.T) {
	server, capture, attempts := newCaptureServer(t, func(int32) int { return http.StatusOK })
	notifier := newTestNotifier(t, server.URL)

	closeNotifier(t, notifier)
	notifier.Notify(testRecord())

	if got := attempts.Load(); got != 0 {
		t.Fatalf("attempts after Close = %d, want 0", got)
	}
	if len(capture.snapshot()) != 0 {
		t.Fatalf("captured after Close = %d, want 0", len(capture.snapshot()))
	}
}
