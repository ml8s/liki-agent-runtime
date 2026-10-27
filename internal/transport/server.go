// Package transport composes standard protocol and operational endpoints.
package transport

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strconv"
	"sync/atomic"
	"time"

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
	A2APrincipal  string
	A2ATenant     string
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

	// Dedicated ops listener: protocol-level mTLS never gates healthchecks
	// or metric scrapers.
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

	switch r.URL.Path {
	case "/ag-ui":
		r = r.WithContext(identity.WithIdentity(r.Context(), verifiedIdentity(r)))
	case "/a2a":
		// A2A accepts either a verified machine identity from mTLS or the
		// composition-configured service principal. Relay user headers are not
		// trusted on this machine protocol surface.
		if mtls := mutualTLSVerified(r); mtls {
			if verified := verifiedIdentity(r); !verified.IsZero() {
				r = r.WithContext(identity.WithIdentity(r.Context(), verified))
			} else if s.services.A2APrincipal != "" {
				r = r.WithContext(identity.WithIdentity(r.Context(), identity.Identity{
					UserID:    s.services.A2APrincipal,
					Tenant:    s.services.A2ATenant,
					MutualTLS: true,
				}))
			}
		} else if s.services.InternalToken != "" && s.services.A2APrincipal != "" {
			r = r.WithContext(identity.WithIdentity(r.Context(), identity.Identity{
				UserID: s.services.A2APrincipal,
				Tenant: s.services.A2ATenant,
			}))
		}
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
