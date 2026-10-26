package agent

import (
	"context"
	"sync"

	platformconfig "github.com/ml8s/liki-agent-runtime/internal/platform/config"

	"google.golang.org/adk/v2/tool"
)

// warmMCPCatalogs pre-discovers each server's tool catalog once at startup so
// the first run after a restart does not pay the lazy cold start (session
// initialize plus tools/list per server). Best effort: failures are ignored
// here because Tools() failure handling already serves the stale catalog or
// propagates the error on demand, and the outcome is observable through the
// catalog metrics. The whole warmup is bounded by one budget so a hung server
// cannot delay startup accounting forever (the goroutine, not startup —
// NewRuntime never blocks on it).
func warmMCPCatalogs(config Config, toolsets []tool.Toolset) {
	budget := config.MCPHealthTimeout
	if budget <= 0 {
		budget = config.MCPTimeout
	}
	if budget <= 0 {
		budget = platformconfig.DefaultMCPTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	go func() {
		defer cancel()
		var wg sync.WaitGroup
		for _, toolset := range toolsets {
			cached, ok := toolset.(*cachedToolset)
			if !ok {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = cached.Tools(systemReadonlyContext{Context: ctx})
			}()
		}
		wg.Wait()
	}()
}
