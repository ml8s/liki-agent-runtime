package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// testDeploymentForMCP builds a minimal deployment for MCP toolset tests.
func testDeploymentForMCP() *deploymentpkg.AgentDeployment {
	return &deploymentpkg.AgentDeployment{Spec: deploymentpkg.DeploymentSpec{
		MCPServers: []deploymentpkg.MCPServerDefinition{{Name: "test", EndpointEnv: "TEST_MCP_ENDPOINT"}},
		Agents: deploymentpkg.ResolvedAgentDefinitions([]deploymentpkg.AgentDefinition{{
			Name:  "test-agent",
			Tools: deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {"test_tool"}}},
		}}),
	}}
}

type catalogTool struct {
	name string
}

func (t catalogTool) Name() string        { return t.name }
func (t catalogTool) Description() string { return t.name + " description" }
func (t catalogTool) IsLongRunning() bool { return false }

// catalogStub is a controllable inner toolset shared by all cachedToolset
// tests. The release channel coordinates concurrent-singleflight timing.
type catalogStub struct {
	mu      sync.Mutex
	calls   int
	tools   []tool.Tool
	err     error
	release chan struct{}
}

func (s *catalogStub) Name() string { return "stub-mcp" }

func (s *catalogStub) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	s.mu.Lock()
	s.calls++
	release := s.release
	s.mu.Unlock()
	if release != nil {
		<-release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.tools, nil
}

func (s *catalogStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newCatalogCache(stub *catalogStub, now func() time.Time, metrics execution.Metrics) *cachedToolset {
	return newCachedToolset("mcp-a", stub, time.Minute, now, metrics)
}

func catalogClock() func() time.Time {
	now := time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

// TestSystemReadonlyContextDefaults pins the zero-value accessors of the
// root-scope context used for warmup catalog discovery.
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

func TestNewCachedToolsetDefaultsTTL(t *testing.T) {
	cache := newCachedToolset("server", &catalogStub{}, 0, nil, nil)
	if cache.ttl != mcpCatalogTTL {
		t.Fatalf("ttl = %v, want default %v", cache.ttl, mcpCatalogTTL)
	}
	if cache.now == nil {
		t.Fatal("now func not defaulted")
	}
}

func TestCachedToolsetForwardsNameAndDefaults(t *testing.T) {
	cache := newCachedToolset("server", &catalogStub{}, time.Minute, nil, nil)
	if cache.Name() != "stub-mcp" {
		t.Fatalf("Name() = %q, want forwarded", cache.Name())
	}
	// Description and IsLongRunning use interface upgrade; catalogStub does
	// not implement them, so the cache must return zero values.
	if cache.Description() != "" {
		t.Fatalf("Description() = %q, want empty", cache.Description())
	}
	if cache.IsLongRunning() {
		t.Fatal("IsLongRunning() = true, want false")
	}
}

func TestMCPCatalogCacheServesWithinTTL(t *testing.T) {
	stub := &catalogStub{tools: []tool.Tool{catalogTool{name: "bazi_chart"}}}
	cache := newCatalogCache(stub, catalogClock(), nil)

	first, err := cache.Tools(nil)
	if err != nil {
		t.Fatalf("first Tools() error = %v", err)
	}
	second, err := cache.Tools(nil)
	if err != nil {
		t.Fatalf("second Tools() error = %v", err)
	}
	if stub.callCount() != 1 {
		t.Fatalf("discovery calls = %d, want 1", stub.callCount())
	}
	if len(first) != 1 || len(second) != 1 || first[0].Name() != "bazi_chart" || second[0].Name() != "bazi_chart" {
		t.Fatalf("cached tools differ: first=%v second=%v", first, second)
	}
}

func TestMCPCatalogCacheRefreshesAfterTTL(t *testing.T) {
	stub := &catalogStub{tools: []tool.Tool{catalogTool{name: "natal_query"}}}
	now := time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC)
	cache := newCatalogCache(stub, func() time.Time { return now }, nil)

	if _, err := cache.Tools(nil); err != nil {
		t.Fatalf("first Tools() error = %v", err)
	}
	now = now.Add(61 * time.Second)
	refreshed, err := cache.Tools(nil)
	if err != nil {
		t.Fatalf("refreshed Tools() error = %v", err)
	}
	if stub.callCount() != 2 {
		t.Fatalf("discovery calls after TTL = %d, want 2", stub.callCount())
	}
	if len(refreshed) != 1 || refreshed[0].Name() != "natal_query" {
		t.Fatalf("refreshed tools = %v", refreshed)
	}
}

func TestMCPCatalogCacheSingleflightsConcurrentRefresh(t *testing.T) {
	stub := &catalogStub{
		tools:   []tool.Tool{catalogTool{name: "compute_factors"}},
		release: make(chan struct{}),
	}
	cache := newCatalogCache(stub, catalogClock(), nil)

	const callers = 8
	results := make(chan error, callers)
	for range callers {
		go func() {
			_, err := cache.Tools(nil)
			results <- err
		}()
	}
	// All callers are now blocked behind the one in-flight discovery.
	time.Sleep(5 * time.Millisecond)
	close(stub.release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Tools() error = %v", err)
		}
	}
	if stub.callCount() != 1 {
		t.Fatalf("discovery calls for concurrent refresh = %d, want 1", stub.callCount())
	}
}

func TestMCPCatalogCacheServesStaleWhenRefreshFails(t *testing.T) {
	stub := &catalogStub{tools: []tool.Tool{catalogTool{name: "city_coords"}}}
	now := time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC)
	cache := newCatalogCache(stub, func() time.Time { return now }, nil)

	if _, err := cache.Tools(nil); err != nil {
		t.Fatalf("first Tools() error = %v", err)
	}
	stub.mu.Lock()
	stub.err = errors.New("mcp unreachable")
	stub.mu.Unlock()
	now = now.Add(2 * time.Minute)

	stale, err := cache.Tools(nil)
	if err != nil {
		t.Fatalf("stale Tools() error = %v", err)
	}
	if len(stale) != 1 || stale[0].Name() != "city_coords" {
		t.Fatalf("stale tools = %v", stale)
	}
}

func TestMCPCatalogCacheFailsClosedWithoutAnyCache(t *testing.T) {
	discoveryErr := errors.New("mcp unreachable")
	stub := &catalogStub{err: discoveryErr}
	metrics := &stubMetrics{}
	cache := newCatalogCache(stub, catalogClock(), metrics)

	if _, err := cache.Tools(nil); !errors.Is(err, discoveryErr) {
		t.Fatalf("Tools() error = %v, want %v", err, discoveryErr)
	}
	if len(metrics.catalog) != 1 || metrics.catalog[0] != "mcp-a/"+mcpCatalogOutcomeError {
		t.Fatalf("catalog observations = %v, want [mcp-a/%s]", metrics.catalog, mcpCatalogOutcomeError)
	}
}

func TestMCPCatalogCacheMetricsLifecycle(t *testing.T) {
	stub := &catalogStub{tools: []tool.Tool{catalogTool{name: "tianwen_time"}}}
	metrics := &stubMetrics{}
	now := time.Date(2026, 10, 26, 11, 0, 0, 0, time.UTC)
	cache := newCatalogCache(stub, func() time.Time { return now }, metrics)

	if _, err := cache.Tools(nil); err != nil {
		t.Fatalf("refresh Tools() error = %v", err)
	}
	if _, err := cache.Tools(nil); err != nil {
		t.Fatalf("hit Tools() error = %v", err)
	}
	stub.mu.Lock()
	stub.err = errors.New("mcp unreachable")
	stub.mu.Unlock()
	now = now.Add(2 * time.Minute)
	if _, err := cache.Tools(nil); err != nil {
		t.Fatalf("stale Tools() error = %v", err)
	}

	want := []string{
		"mcp-a/" + mcpCatalogOutcomeRefresh,
		"mcp-a/" + mcpCatalogOutcomeHit,
		"mcp-a/" + mcpCatalogOutcomeStale,
	}
	if len(metrics.catalog) != len(want) {
		t.Fatalf("catalog observations = %v, want %v", metrics.catalog, want)
	}
	for index, outcome := range want {
		if metrics.catalog[index] != outcome {
			t.Fatalf("catalog observation %d = %q, want %q", index, metrics.catalog[index], outcome)
		}
	}
}

func TestCachedToolsetNilMetricsSafe(t *testing.T) {
	cache := newCachedToolset("server", &catalogStub{}, time.Minute, catalogClock(), nil)
	if _, err := cache.Tools(nil); err != nil {
		t.Fatalf("Tools() with nil metrics error = %v", err)
	}
}

// TestNewMCPToolsetsMemoizeDiscovery wires the cache exactly as the runtime
// graph does (FilterToolset over the cached toolset) against a real in-memory
// MCP server, and proves repeated flows share one tools/list round.
func TestNewMCPToolsetsMemoizeDiscovery(t *testing.T) {
	t.Setenv("TEST_MCP_ENDPOINT", "http://test.invalid/mcp")

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-mcp", Version: "test"},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{},
		}},
	)
	var listCalls atomic.Int32
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				listCalls.Add(1)
			}
			return next(ctx, method, request)
		}
	})
	server.AddTool(&mcp.Tool{
		Name:        "test_tool",
		Description: "test deterministic tool",
		InputSchema: &jsonschema.Schema{Type: "object"},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	deployment := testDeploymentForMCP()
	config := Config{
		MCPTimeout: 5 * time.Second,
		mcpTransportOverrides: map[string]mcp.Transport{
			"test": clientTransport,
		},
		AgentDeployment: deployment,
		Now:             catalogClock(),
	}
	toolsets, err := newMCPToolsets(config)
	if err != nil {
		t.Fatalf("newMCPToolsets() error = %v", err)
	}
	if len(toolsets) != 1 {
		t.Fatalf("toolsets = %d, want 1", len(toolsets))
	}
	// Mirror agent graph construction: allowlist filtering after the cache.
	filtered := tool.FilterToolset(toolsets[0], tool.AllowedToolsPredicate([]string{"test_tool"}))

	ctx := systemReadonlyContext{Context: context.Background()}
	for flow := 0; flow < 3; flow++ {
		tools, err := filtered.Tools(ctx)
		if err != nil {
			t.Fatalf("flow %d Tools() error = %v", flow, err)
		}
		if len(tools) != 1 || tools[0].Name() != "test_tool" {
			t.Fatalf("flow %d tools = %v, want test_tool", flow, tools)
		}
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("tools/list calls for 3 flows = %d, want 1", got)
	}
}
