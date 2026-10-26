package agent

import (
	"context"
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/execution"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// systemReadonlyContext is a minimal root-scope ReadonlyContext for catalog
// calls the runtime initiates outside any agent invocation (startup warmup).
// The MCP list call only uses the context for cancellation and deadlines.
type systemReadonlyContext struct{ context.Context }

func (systemReadonlyContext) UserContent() *genai.Content { return nil }
func (systemReadonlyContext) InvocationID() string        { return "" }
func (systemReadonlyContext) AgentName() string           { return "" }

func (systemReadonlyContext) ReadonlyState() session.ReadonlyState {
	return nil
}

func (systemReadonlyContext) UserID() string    { return "" }
func (systemReadonlyContext) AppName() string   { return "" }
func (systemReadonlyContext) SessionID() string { return "" }
func (systemReadonlyContext) Branch() string    { return "" }

// mcpCatalogTTL bounds how long one standard tools/list discovery round is
// reused. AgentDeployment artifacts are immutable; a server-side catalog changes
// only when the MCP server itself redeploys, so one minute bounds staleness
// while removing an outbound round trip from every agent flow.
const mcpCatalogTTL = time.Minute

// Catalog cache outcomes, used as stable metric labels.
const (
	mcpCatalogOutcomeHit     = "hit"
	mcpCatalogOutcomeRefresh = "refresh"
	mcpCatalogOutcomeStale   = "stale"
	mcpCatalogOutcomeError   = "error"
)

// cachedToolset memoizes one MCP server's discovered and converted tool list.
// It is a transparent decorator over the official ADK mcptoolset:
//
//   - discovery stays standard `tools/list`; only its result is memoized;
//   - allowlist filtering runs after this cache, exactly as before, inside
//     tool.FilterToolset — the cache never authorizes anything;
//   - concurrent flows share one in-flight refresh;
//   - a failed refresh serves the last known catalog instead of failing runs
//     at tool-declaration time; actual tool calls still use the live
//     fast-fail transport;
//   - with no cached catalog, discovery failures propagate unchanged.
type cachedToolset struct {
	server  string
	inner   tool.Toolset
	ttl     time.Duration
	now     func() time.Time
	metrics execution.Metrics

	mu         sync.Mutex
	tools      []tool.Tool
	fetchedAt  time.Time
	valid      bool
	lastErr    error
	refreshing chan struct{}
}

func newCachedToolset(
	server string,
	inner tool.Toolset,
	ttl time.Duration,
	now func() time.Time,
	metrics execution.Metrics,
) *cachedToolset {
	if ttl <= 0 {
		ttl = mcpCatalogTTL
	}
	if now == nil {
		now = time.Now
	}
	return &cachedToolset{
		server:  server,
		inner:   inner,
		ttl:     ttl,
		now:     now,
		metrics: metrics,
	}
}

// Name forwards the wrapped toolset identity.
func (c *cachedToolset) Name() string { return c.inner.Name() }

// Description forwards the wrapped description when the inner toolset
// exposes one.
func (c *cachedToolset) Description() string {
	if describable, ok := c.inner.(interface{ Description() string }); ok {
		return describable.Description()
	}
	return ""
}

// IsLongRunning forwards the wrapped answer when the inner toolset exposes
// one.
func (c *cachedToolset) IsLongRunning() bool {
	if longRunning, ok := c.inner.(interface{ IsLongRunning() bool }); ok {
		return longRunning.IsLongRunning()
	}
	return false
}

// Tools returns the memoized catalog, refreshing under singleflight when the
// entry is expired.
func (c *cachedToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	c.mu.Lock()
	if c.valid && c.now().Sub(c.fetchedAt) < c.ttl {
		tools := c.tools
		c.mu.Unlock()
		c.observe(mcpCatalogOutcomeHit)
		return tools, nil
	}
	done := c.refreshing
	leader := false
	if done == nil {
		leader = true
		done = make(chan struct{})
		c.refreshing = done
	}
	c.mu.Unlock()

	if leader {
		return c.refresh(ctx, done)
	}
	<-done
	c.mu.Lock()
	tools, valid, lastErr := c.tools, c.valid, c.lastErr
	c.mu.Unlock()
	if valid {
		c.observe(mcpCatalogOutcomeHit)
		return tools, nil
	}
	c.observe(mcpCatalogOutcomeError)
	return nil, lastErr
}

func (c *cachedToolset) refresh(ctx agent.ReadonlyContext, done chan struct{}) ([]tool.Tool, error) {
	tools, err := c.inner.Tools(ctx)
	c.mu.Lock()
	if err == nil {
		c.tools = tools
		c.fetchedAt = c.now()
		c.valid = true
		c.lastErr = nil
	} else {
		c.lastErr = err
	}
	close(done)
	c.refreshing = nil
	valid := c.valid
	cached := c.tools
	c.mu.Unlock()

	switch {
	case err == nil:
		c.observe(mcpCatalogOutcomeRefresh)
		return tools, nil
	case valid:
		// Availability beats freshness for declarations: the live transport
		// still owns tool-call execution and fails fast on a removed tool.
		c.observe(mcpCatalogOutcomeStale)
		return cached, nil
	default:
		c.observe(mcpCatalogOutcomeError)
		return nil, err
	}
}

func (c *cachedToolset) observe(outcome string) {
	if c.metrics != nil {
		c.metrics.ObserveMCPCatalog(c.server, outcome)
	}
}
