package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

func TestSystemReadonlyContextDefaults(t *testing.T) {
	ctx := systemReadonlyContext{Context: context.Background()}
	if ctx.UserContent() != nil {
		t.Fatal("UserContent() != nil")
	}
	if ctx.InvocationID() != "" || ctx.AgentName() != "" {
		t.Fatal("InvocationID/AgentName not empty")
	}
	if ctx.ReadonlyState() != nil {
		t.Fatal("ReadonlyState() != nil")
	}
	if ctx.UserID() != "" || ctx.AppName() != "" || ctx.SessionID() != "" || ctx.Branch() != "" {
		t.Fatal("identity accessors not empty")
	}
}

// catalogStubTool satisfies tool.Tool for cache assertions.
type catalogStubTool struct{ name string }

func (s *catalogStubTool) Name() string        { return s.name }
func (s *catalogStubTool) Description() string { return "stub" }
func (s *catalogStubTool) IsLongRunning() bool { return false }

// catalogTestToolset is a controllable inner toolset.
type catalogTestToolset struct {
	name  string
	tools []tool.Tool
	err   error
	calls int
}

func (s *catalogTestToolset) Name() string { return s.name }

func (s *catalogTestToolset) Tools(_ agent.ReadonlyContext) ([]tool.Tool, error) {
	s.calls++
	return s.tools, s.err
}

// catalogMetrics implements execution.Metrics, counting catalog outcomes.
type catalogMetrics struct {
	outcomes map[string]int
}

func (m *catalogMetrics) ObserveMCPCatalog(_, outcome string) {
	if m.outcomes == nil {
		m.outcomes = map[string]int{}
	}
	m.outcomes[outcome]++
}
func (m *catalogMetrics) ObserveLLMCall(_, _ string, _ domain.LLMTokenUsage)     {}
func (m *catalogMetrics) ObserveLLMCallLatency(_, _ string, _ time.Duration)     {}
func (m *catalogMetrics) ObserveToolCall(_, _, _ string, _ time.Duration)        {}
func (m *catalogMetrics) ObserveAgentDelegation(_, _, _ string, _ time.Duration) {}
func (m *catalogMetrics) ObserveRunRejection(_ string)                           {}

func TestCachedToolsetForwardsNameAndDefaults(t *testing.T) {
	inner := &catalogTestToolset{name: "test"}
	cache := newCachedToolset("server", inner, time.Minute, nil, nil)

	if cache.Name() != "test" {
		t.Fatalf("Name() = %q, want forwarded", cache.Name())
	}
	// Description and IsLongRunning use interface upgrade; the stub does not
	// implement them, so the cache must return zero values.
	if cache.Description() != "" {
		t.Fatalf("Description() = %q, want empty", cache.Description())
	}
	if cache.IsLongRunning() {
		t.Fatal("IsLongRunning() = true, want false")
	}
}

func TestNewCachedToolsetDefaultsTTL(t *testing.T) {
	cache := newCachedToolset("server", &catalogTestToolset{}, 0, nil, nil)
	if cache.ttl != mcpCatalogTTL {
		t.Fatalf("ttl = %v, want default %v", cache.ttl, mcpCatalogTTL)
	}
	if cache.now == nil {
		t.Fatal("now func not defaulted")
	}
}

func TestCachedToolsetRefreshThenHit(t *testing.T) {
	inner := &catalogTestToolset{name: "test"}
	metrics := &catalogMetrics{}
	tick := time.Unix(1000, 0)
	cache := newCachedToolset("server", inner, time.Minute, func() time.Time { return tick }, metrics)
	ctx := systemReadonlyContext{Context: context.Background()}

	if _, err := cache.Tools(ctx); err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}

	if _, err := cache.Tools(ctx); err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls after hit = %d, want still 1", inner.calls)
	}
	if metrics.outcomes["hit"] != 1 || metrics.outcomes["refresh"] != 1 {
		t.Fatalf("outcomes = %v, want 1 refresh + 1 hit", metrics.outcomes)
	}
}

func TestCachedToolsetServesStaleOnRefreshError(t *testing.T) {
	inner := &catalogTestToolset{
		name:  "test",
		tools: []tool.Tool{&catalogStubTool{name: "tool_a"}, &catalogStubTool{name: "tool_b"}},
	}
	metrics := &catalogMetrics{}
	tick := time.Unix(1000, 0)
	cache := newCachedToolset("server", inner, time.Minute, func() time.Time { return tick }, metrics)
	ctx := systemReadonlyContext{Context: context.Background()}

	if _, err := cache.Tools(ctx); err != nil {
		t.Fatalf("initial Tools() error = %v", err)
	}

	tick = tick.Add(2 * time.Minute)
	inner.err = errors.New("discovery down")

	tools, err := cache.Tools(ctx)
	if err != nil {
		t.Fatalf("stale Tools() error = %v, want served from cache", err)
	}
	if len(tools) != 2 {
		t.Fatalf("stale tools = %d, want 2", len(tools))
	}
	if metrics.outcomes["stale"] != 1 {
		t.Fatalf("stale outcome = %d, want 1", metrics.outcomes["stale"])
	}
}

func TestCachedToolsetPropagatesErrorWithoutCache(t *testing.T) {
	inner := &catalogTestToolset{name: "test", err: errors.New("unavailable")}
	metrics := &catalogMetrics{}
	cache := newCachedToolset("server", inner, time.Minute, func() time.Time { return time.Unix(1, 0) }, metrics)
	ctx := systemReadonlyContext{Context: context.Background()}

	if _, err := cache.Tools(ctx); err == nil {
		t.Fatal("Tools() accepted a failed discovery with no cache")
	}
	if metrics.outcomes["error"] != 1 {
		t.Fatalf("error outcome = %d, want 1", metrics.outcomes["error"])
	}
}

func TestCachedToolsetConcurrentSingleflight(t *testing.T) {
	inner := &catalogTestToolset{name: "test"}
	cache := newCachedToolset("server", inner, time.Minute, func() time.Time { return time.Unix(1, 0) }, nil)
	ctx := systemReadonlyContext{Context: context.Background()}

	const goroutines = 8
	errs := make(chan error, goroutines)
	for range goroutines {
		go func() {
			_, err := cache.Tools(ctx)
			errs <- err
		}()
	}
	for range goroutines {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Tools() error = %v", err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("inner discovery calls = %d, want 1 (singleflight)", inner.calls)
	}
}

func TestCachedToolsetNilMetricsSafe(t *testing.T) {
	inner := &catalogTestToolset{name: "test"}
	cache := newCachedToolset("server", inner, time.Minute, func() time.Time { return time.Unix(1, 0) }, nil)
	ctx := systemReadonlyContext{Context: context.Background()}
	if _, err := cache.Tools(ctx); err != nil {
		t.Fatalf("Tools() with nil metrics error = %v", err)
	}
}
