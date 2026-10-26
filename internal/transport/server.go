// Package transport composes standard protocol and operational endpoints.
package transport

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"github.com/ml8s/liki-agent-runtime/internal/observability"
	"github.com/ml8s/liki-agent-runtime/internal/platform"
	"github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo"
	"github.com/ml8s/liki-agent-runtime/internal/platform/identity"
)

// Services bundles the protocol and operational handlers the transport
// composes.
type Services struct {
	AgentCard     http.Handler
	A2A           http.Handler
	AGUI          http.Handler
	HealthChecks  []platform.HealthChecker
	Metrics       observability.ProtocolMetrics
	Dependencies  observability.DependencyMetrics
	MetricsTarget http.Handler
	InternalToken string
	Logger        *slog.Logger

	// ReadinessCacheTTL bounds how long one dependency check round is reused.
	// Zero selects the default; negative values disable caching.
	ReadinessCacheTTL time.Duration
}

// Server owns the HTTP routing, authentication, throttling, and readiness
// surface.
type Server struct {
	services       Services
	routes         *http.ServeMux
	opsRoutes      *http.ServeMux
	readiness      atomic.Bool
	authFailures   *authFailureLimiter
	readinessCache *readinessCache
}

const (
	authFailureLimit      = 10
	authFailureWindow     = time.Minute
	maxAuthFailureSources = 10000
	readinessBudget       = 1500 * time.Millisecond
	defaultReadinessTTL   = 10 * time.Second
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

type authFailure struct {
	count       int
	windowStart time.Time
}

type authFailureLimiter struct {
	mu      sync.Mutex
	entries map[string]authFailure
}

func newAuthFailureLimiter() *authFailureLimiter {
	return &authFailureLimiter{entries: make(map[string]authFailure)}
}

// tryConsume records one failure and reports whether the source still has
// budget. Keeping the decision and increment under one lock prevents concurrent
// requests from exceeding the configured threshold.
func (l *authFailureLimiter) tryConsume(source string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	record, exists := l.entries[source]
	if !exists || now.Sub(record.windowStart) >= authFailureWindow {
		if len(l.entries) >= maxAuthFailureSources {
			l.pruneLocked(now)
		}
		if len(l.entries) >= maxAuthFailureSources {
			var oldestSource string
			var oldest time.Time
			for candidateSource, candidate := range l.entries {
				if oldest.IsZero() || candidate.windowStart.Before(oldest) {
					oldestSource, oldest = candidateSource, candidate.windowStart
				}
			}
			delete(l.entries, oldestSource)
		}
		record = authFailure{windowStart: now}
	}
	if record.count >= authFailureLimit {
		l.entries[source] = record
		return false
	}
	record.count++
	l.entries[source] = record
	return true
}

func (l *authFailureLimiter) clear(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, source)
}

func (l *authFailureLimiter) pruneLocked(now time.Time) {
	for source, record := range l.entries {
		if now.Sub(record.windowStart) >= authFailureWindow {
			delete(l.entries, source)
		}
	}
}

// New composes the standard protocol and operational endpoints.
func New(services Services) (*Server, error) {
	if services.AgentCard == nil || services.A2A == nil || services.AGUI == nil {
		return nil, fmt.Errorf("A2A and AG-UI protocol handlers are required")
	}
	if services.Logger == nil {
		services.Logger = slog.Default()
	}
	readinessTTL := services.ReadinessCacheTTL
	if readinessTTL == 0 {
		readinessTTL = defaultReadinessTTL
	}

	server := &Server{
		services:       services,
		authFailures:   newAuthFailureLimiter(),
		readinessCache: &readinessCache{ttl: readinessTTL},
	}
	server.readiness.Store(true)

	api := http.NewServeMux()
	api.Handle("GET /.well-known/agent-card.json", server.services.AgentCard)
	api.Handle("POST /a2a", server.authorized(server.bodyLimit(server.services.A2A), false))
	api.Handle("POST /ag-ui", server.authorized(server.bodyLimit(server.services.AGUI), true))

	// 运维面独立监听：协议面的 mTLS 不波及健康检查与指标抓取。
	ops := http.NewServeMux()
	ops.HandleFunc("GET /readyz", server.ready)
	ops.HandleFunc("GET /version", server.version)
	ops.Handle("GET /metrics", server.authorized(server.metricsHandler(), false))
	// Profiling shares the ops listener and the metrics authentication policy:
	// open to operators, token-gated whenever the internal token is set.
	// pprof.Index also serves named profiles such as /debug/pprof/heap.
	ops.Handle("/debug/pprof/", server.authorized(http.HandlerFunc(pprof.Index), false))
	ops.Handle("/debug/pprof/cmdline", server.authorized(http.HandlerFunc(pprof.Cmdline), false))
	ops.Handle("/debug/pprof/profile", server.authorized(http.HandlerFunc(pprof.Profile), false))
	ops.Handle("/debug/pprof/symbol", server.authorized(http.HandlerFunc(pprof.Symbol), false))
	ops.Handle("/debug/pprof/trace", server.authorized(http.HandlerFunc(pprof.Trace), false))

	server.routes = api
	server.opsRoutes = ops
	return server, nil
}

// OpsHandler returns the operational surface (/readyz, /version, /metrics).
// It is served on a dedicated listener so protocol-level mutual TLS never
// gates healthchecks or metric scrapers.
func (s *Server) OpsHandler() http.Handler {
	return s.opsRoutes
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := r.Header.Get("X-Request-ID")
	if !validRequestID(requestID) {
		requestID = randomID()
	}
	writer := &responseWriter{ResponseWriter: w, status: http.StatusOK}
	writer.Header().Set("X-Request-ID", requestID)
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	// The verified user identity header is a liki-web AG-UI trust boundary. It
	// is only adopted on /ag-ui; A2A machine callers sharing the internal token
	// must not be able to spoof a user identity into audit/provenance.
	if r.URL.Path == "/ag-ui" {
		r = r.WithContext(identity.WithIdentity(r.Context(), verifiedIdentity(r)))
	}

	s.routes.ServeHTTP(writer, r)
	s.observe(r.URL.Path, r.Method, writer.status, time.Since(start))
}

// Drain removes this process from the readiness rotation. It does not cancel
// active requests; the HTTP server shutdown deadline still owns draining them.
func (s *Server) Drain() {
	s.readiness.Store(false)
}

func (s *Server) bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) observe(path, method string, status int, duration time.Duration) {
	if s.services.Metrics == nil {
		return
	}
	protocol, operation, ok := protocolOperation(path, method)
	if !ok {
		return
	}
	s.services.Metrics.ObserveHTTPRequest(protocol, operation, strconv.Itoa(status), duration)
}

func protocolOperation(path, method string) (protocol, operation string, ok bool) {
	switch {
	case path == "/a2a" && method == http.MethodPost:
		return "a2a", "message", true
	case path == "/ag-ui" && method == http.MethodPost:
		return "ag_ui", "run", true
	case path == "/.well-known/agent-card.json" && method == http.MethodGet:
		return "a2a", "agent_card", true
	default:
		return "", "", false
	}
}

func (s *Server) authorized(next http.Handler, requireIdentity bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source := requestSource(r.RemoteAddr)
		// mTLS 通道（客户端证书已验证）以证书为通道凭证；明文监听器（过渡期
		// 中继）仍要求服务 token。
		if s.services.InternalToken != "" && !mutualTLSVerified(r) {
			const prefix = "Bearer "
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, prefix) || subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(s.services.InternalToken)) != 1 {
				if !s.authFailures.tryConsume(source, time.Now()) {
					w.Header().Set("Retry-After", "60")
					writeError(w, http.StatusTooManyRequests, domain.CodeRateLimited, "too many authentication failures")
					return
				}
				writeError(w, http.StatusUnauthorized, domain.CodeUnauthorized, "valid service token required")
				return
			}
		}
		verified, _ := identity.FromContext(r.Context())
		if requireIdentity && verified.IsZero() {
			if !s.authFailures.tryConsume(source, time.Now()) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, domain.CodeRateLimited, "too many authentication failures")
				return
			}
			writeError(w, http.StatusUnauthorized, domain.CodeIdentityRequired, "verified user identity required")
			return
		}
		s.authFailures.clear(source)
		next.ServeHTTP(w, r)
	})
}

func requestSource(remoteAddr string) string {
	if remoteAddr == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil && host != "" {
		remoteAddr = host
	}
	if len(remoteAddr) > 128 {
		remoteAddr = remoteAddr[:128]
	}
	return remoteAddr
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

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service":    "liki-agent-runtime",
		"version":    buildinfo.Version,
		"commit":     buildinfo.Commit,
		"build_time": buildinfo.BuildTime,
		"protocols":  []string{"a2a", "ag-ui", "mcp-client"},
	})
}

func (s *Server) metricsHandler() http.Handler {
	if s.services.MetricsTarget != nil {
		return s.services.MetricsTarget
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
}

func verifiedIdentity(r *http.Request) identity.Identity {
	// 通道决定受信头（契约 C4）：
	//   mTLS（边缘 forward_auth 注入）→ Remote-User；
	//   明文（带 token 的中继调用方）→ X-Liki-User-ID。
	mtls := mutualTLSVerified(r)
	header := "X-Liki-User-ID"
	if mtls {
		header = "Remote-User"
	}
	userID := strings.TrimSpace(r.Header.Get(header))
	if !validRequestID(userID) {
		return identity.Identity{}
	}
	return identity.Identity{UserID: userID, MutualTLS: mtls}
}

// mutualTLSVerified reports whether the request arrived over a TLS connection
// whose client certificate the listener already verified. The mTLS listener is
// configured with RequireAndVerifyClientCert, so a present peer certificate
// means the channel itself is authenticated.
func mutualTLSVerified(r *http.Request) bool {
	return r.TLS != nil && len(r.TLS.PeerCertificates) > 0
}

func validRequestID(value string) bool {
	return domain.ValidIdentifier(value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	// RFC 9457 problem+json payload with the runtime's stable code in the
	// standard extension field.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
		"detail": message,
	})
}
