package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

func TestAcquireRunObservesRejectionReasons(t *testing.T) {
	metrics := &lifecycleStubMetrics{}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	runtime := &Runtime{
		config:   Config{Metrics: metrics},
		runSlots: make(chan struct{}, 1),
	}
	if _, err := runtime.acquireRun(cancelled); err == nil {
		t.Fatal("acquireRun(canceled context) unexpectedly succeeded")
	}
	runtime.runSlots <- struct{}{}
	if _, err := runtime.acquireRun(context.Background()); err == nil {
		t.Fatal("acquireRun(full slots) unexpectedly succeeded")
	}
	want := []string{"run_rejection/cancelled", "run_rejection/busy"}
	if !slices.Equal(metrics.calls, want) {
		t.Fatalf("observed rejections = %v, want %v", metrics.calls, want)
	}
}

type lifecycleStubMetrics struct {
	calls []string
}

func (m *lifecycleStubMetrics) ObserveAgentDelegation(string, string, string, time.Duration) {}

func (m *lifecycleStubMetrics) ObserveLLMCall(string, string, domain.LLMTokenUsage)   {}
func (m *lifecycleStubMetrics) ObserveLLMCallLatency(string, string, time.Duration)   {}
func (m *lifecycleStubMetrics) ObserveToolCall(string, string, string, time.Duration) {}
func (m *lifecycleStubMetrics) ObserveMCPCatalog(string, string)                      {}
func (m *lifecycleStubMetrics) ObserveRunRejection(reason string) {
	m.calls = append(m.calls, "run_rejection/"+reason)
}
