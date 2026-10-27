package transport

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/ml8s/agent-runtime/internal/platform"
)

const (
	readinessBudget     = 1500 * time.Millisecond
	defaultReadinessTTL = 10 * time.Second
)

type readinessSnapshot struct {
	health    []platform.DependencyHealth
	unhealthy []string
}

// readinessCache deduplicates dependency checks across probe scrapes. Probes
// arrive far more often than dependencies change, and every uncached round
// costs a full outbound MCP discovery; concurrent scrapes share one refresh.
type readinessCache struct {
	ttl       time.Duration
	mu        sync.Mutex
	snapshot  readinessSnapshot
	fetchedAt time.Time
	valid     bool
	inflight  chan struct{}
}

func (c *readinessCache) get(
	ctx context.Context,
	collect func(context.Context) readinessSnapshot,
) readinessSnapshot {
	if c == nil || c.ttl <= 0 {
		return collect(ctx)
	}
	c.mu.Lock()
	if c.valid && time.Since(c.fetchedAt) < c.ttl {
		snapshot := c.snapshot
		c.mu.Unlock()
		return snapshot
	}
	done := c.inflight
	leader := false
	if done == nil {
		leader = true
		done = make(chan struct{})
		c.inflight = done
	}
	c.mu.Unlock()

	if leader {
		snapshot := collect(ctx)
		c.mu.Lock()
		c.snapshot = snapshot
		c.fetchedAt = time.Now()
		c.valid = true
		close(done)
		c.inflight = nil
		c.mu.Unlock()
		return snapshot
	}

	select {
	case <-done:
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid {
		// The first refresh is still running; readiness fails closed instead of
		// reporting an unverified dependency set.
		return readinessSnapshot{unhealthy: []string{"readiness_checks_incomplete"}}
	}
	return c.snapshot
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if !s.readiness.Load() {
		if s.services.Dependencies != nil {
			s.services.Dependencies.SetDependencyReady("runtime", false)
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	// A disconnected readiness client is not a dependency failure. Health
	// checks own their deadlines and must run with request values but without
	// the request's cancellation signal.
	checkContext, cancelChecks := context.WithTimeout(
		context.WithoutCancel(r.Context()),
		readinessBudget,
	)
	defer cancelChecks()
	snapshot := s.readinessCache.get(checkContext, s.collectReadiness)
	for _, health := range snapshot.health {
		if s.services.Dependencies != nil {
			s.services.Dependencies.SetDependencyReady(health.Name, health.OK)
		}
	}
	if len(snapshot.unhealthy) != 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":       "unavailable",
			"dependencies": snapshot.unhealthy,
		})
		return
	}
	if s.services.Dependencies != nil {
		s.services.Dependencies.SetDependencyReady("runtime", true)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// collectReadiness runs one dependency check round. Failure detail stays in
// logs; the HTTP surface exposes dependency names only.
func (s *Server) collectReadiness(ctx context.Context) readinessSnapshot {
	healthResults := make([]platform.DependencyHealth, len(s.services.HealthChecks))
	var wait sync.WaitGroup
	for index, checker := range s.services.HealthChecks {
		wait.Add(1)
		go func() {
			defer wait.Done()
			healthResults[index] = checker.CheckHealth(ctx)
		}()
	}
	wait.Wait()

	unhealthy := make([]string, 0)
	for _, health := range healthResults {
		if !health.OK {
			s.services.Logger.Warn("readiness dependency unhealthy",
				"dependency", health.Name, "detail", health.Detail)
			unhealthy = append(unhealthy, health.Name)
		}
	}
	return readinessSnapshot{health: healthResults, unhealthy: unhealthy}
}
