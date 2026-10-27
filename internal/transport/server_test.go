package transport_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/domain"
	"github.com/ml8s/agent-runtime/internal/observability"
	"github.com/ml8s/agent-runtime/internal/platform"
	"github.com/ml8s/agent-runtime/internal/platform/identity"
	"github.com/ml8s/agent-runtime/internal/transport"
)

type healthy struct{}

func (healthy) CheckHealth(context.Context) platform.DependencyHealth {
	return platform.DependencyHealth{Name: "test", OK: true}
}

type countingChecker struct {
	count atomic.Int32
}

func (c *countingChecker) CheckHealth(context.Context) platform.DependencyHealth {
	c.count.Add(1)
	return platform.DependencyHealth{Name: "counted", OK: true}
}

func newServer(t *testing.T, token string) *transport.Server {
	t.Helper()
	handler, err := transport.New(transport.Services{
		AgentCard:     http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		HealthChecks:  []platform.HealthChecker{healthy{}},
		InternalToken: token,
	})
	if err != nil {
		t.Fatalf("transport.New() error = %v", err)
	}
	return handler
}

func mustNewServer(t *testing.T, services transport.Services) *transport.Server {
	t.Helper()
	handler, err := transport.New(services)
	if err != nil {
		t.Fatalf("transport.New() error = %v", err)
	}
	return handler
}

type recordingMetrics struct {
	requests []string
}

var _ observability.ProtocolMetrics = (*recordingMetrics)(nil)

func (m *recordingMetrics) ObserveHTTPRequest(protocol, operation, status string, _ time.Duration) {
	m.requests = append(m.requests, protocol+"/"+operation+"/"+status)
}

func (*recordingMetrics) StreamOpened(string) {}
func (*recordingMetrics) StreamClosed(string) {}

func TestProtocolRequestsAreObserved(t *testing.T) {
	metrics := &recordingMetrics{}
	handler := mustNewServer(t, transport.Services{
		AgentCard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:       http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:      http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		Metrics:   metrics,
	})
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	request := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{}`))
	request.Header.Set("X-Agent-User-ID", "user_1")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if len(metrics.requests) != 2 ||
		metrics.requests[0] != "a2a/agent_card/200" ||
		metrics.requests[1] != "a2a/message/202" {
		t.Fatalf("metrics = %v", metrics.requests)
	}
}

func TestProtocolRoutesRequireServiceToken(t *testing.T) {
	handler := newServer(t, "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("authenticated status = %d", response.Code)
	}
}

func TestAGUIRequiresVerifiedIdentity(t *testing.T) {
	handler := newServer(t, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(`{}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("identityless status = %d", response.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(`{}`))
	request.Header.Set("X-Agent-User-ID", "user_1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("identified status = %d", response.Code)
	}
}

func TestOperationalEndpointsAreOwnedByOpsHandler(t *testing.T) {
	handler := newServer(t, "secret")
	ops := handler.OpsHandler()
	for _, path := range []string{"/readyz", "/version"} {
		response := httptest.NewRecorder()
		ops.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("ops %s status = %d", path, response.Code)
		}
		// API 监听器不再服务运维端点（mTLS 下健康检查不受影响）。
		apiResponse := httptest.NewRecorder()
		handler.ServeHTTP(apiResponse, httptest.NewRequest(http.MethodGet, path, nil))
		if apiResponse.Code != http.StatusNotFound {
			t.Fatalf("api %s status = %d, want 404", path, apiResponse.Code)
		}
	}
}

func TestMetricsRequiresServiceToken(t *testing.T) {
	handler := mustNewServer(t, transport.Services{
		AgentCard:     http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		MetricsTarget: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		InternalToken: "secret",
	})

	unauthenticated := httptest.NewRecorder()
	handler.OpsHandler().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics status = %d", unauthenticated.Code)
	}

	authenticated := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	authenticated.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.OpsHandler().ServeHTTP(response, authenticated)
	if response.Code != http.StatusTeapot {
		t.Fatalf("authenticated metrics status = %d", response.Code)
	}
}

func TestAuthenticationFailuresAreRateLimitedByRemoteSource(t *testing.T) {
	handler := newServer(t, "secret")
	request := func(remoteAddr string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{}`))
		request.RemoteAddr = remoteAddr
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	for attempt := 1; attempt <= 10; attempt++ {
		if response := request("192.0.2.10:12345"); response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, http.StatusUnauthorized)
		}
	}
	blocked := request("192.0.2.10:54321")
	if blocked.Code != http.StatusTooManyRequests || !strings.Contains(blocked.Body.String(), domain.CodeRateLimited) {
		t.Fatalf("blocked response = %d %s", blocked.Code, blocked.Body.String())
	}

	// Forwarded headers are untrusted and must not bypass the socket source.
	forwarded := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{}`))
	forwarded.RemoteAddr = "192.0.2.10:12345"
	forwarded.Header.Set("X-Forwarded-For", "198.51.100.1")
	forwardedResponse := httptest.NewRecorder()
	handler.ServeHTTP(forwardedResponse, forwarded)
	if forwardedResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("forwarded source status = %d, want %d", forwardedResponse.Code, http.StatusTooManyRequests)
	}

	otherSource := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{}`))
	otherSource.RemoteAddr = "198.51.100.1:12345"
	otherSource.Header.Set("Authorization", "Bearer secret")
	otherResponse := httptest.NewRecorder()
	handler.ServeHTTP(otherResponse, otherSource)
	if otherResponse.Code != http.StatusAccepted {
		t.Fatalf("other source status = %d", otherResponse.Code)
	}
}

func TestIdentityFailuresAreRateLimitedWithoutServiceToken(t *testing.T) {
	handler := newServer(t, "")
	for attempt := 1; attempt <= 10; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(`{}`))
		request.RemoteAddr = "198.51.100.10:12345"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, http.StatusUnauthorized)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(`{}`))
	request.RemoteAddr = "198.51.100.10:54321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), domain.CodeRateLimited) {
		t.Fatalf("blocked response = %d %s", response.Code, response.Body.String())
	}
}

func TestDrainMakesReadinessUnavailableWithoutCancellingRequests(t *testing.T) {
	server, err := transport.New(transport.Services{
		AgentCard:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		HealthChecks: []platform.HealthChecker{healthy{}},
	})
	if err != nil {
		t.Fatalf("transport.New() error = %v", err)
	}
	server.Drain()

	readiness := httptest.NewRecorder()
	server.OpsHandler().ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusServiceUnavailable {
		t.Fatalf("drained readiness status = %d, want %d", readiness.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(readiness.Body.String(), `"draining"`) {
		t.Fatalf("drained readiness body = %s", readiness.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(`{}`))
	request.Header.Set("X-Agent-User-ID", "user_1")
	protocol := httptest.NewRecorder()
	server.ServeHTTP(protocol, request)
	if protocol.Code != http.StatusAccepted {
		t.Fatalf("in-flight protocol status after Drain() = %d", protocol.Code)
	}
}

func TestHealthzIsRetired(t *testing.T) {
	handler := newServer(t, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("healthz status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestA2ABodySizeLimit(t *testing.T) {
	readBody := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	handler := mustNewServer(t, transport.Services{
		AgentCard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:       readBody,
		AGUI:      http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
	})
	bigBody := strings.Repeat("x", 2<<20+1)
	request := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(bigBody))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body too large status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestAuthenticationRunsBeforeRequestBodyIsConsumed(t *testing.T) {
	read := false
	handler := mustNewServer(t, transport.Services{
		AgentCard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			read = true
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusAccepted)
		}),
		AGUI:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		InternalToken: "secret",
	})
	request := httptest.NewRequest(http.MethodPost, "/a2a", strings.NewReader(strings.Repeat("x", 2<<20+1)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated oversized status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if read {
		t.Fatal("protocol handler read an unauthenticated request body")
	}
}

func TestAGUIRejectsOversizedIdentityHeader(t *testing.T) {
	handler := newServer(t, "")
	request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(`{}`))
	request.Header.Set("X-Agent-User-ID", strings.Repeat("u", 129))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("oversized identity status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestReadyzDoesNotLeakDependencyDetail(t *testing.T) {
	unhealthy := &unhealthyChecker{name: "mcp_a", detail: "dial tcp 10.0.0.1:18081: connection refused"}
	handler := mustNewServer(t, transport.Services{
		AgentCard:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		HealthChecks: []platform.HealthChecker{unhealthy},
	})
	response := httptest.NewRecorder()
	handler.OpsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	body := response.Body.String()
	if strings.Contains(body, "dial tcp") || strings.Contains(body, "connection refused") {
		t.Fatalf("readyz leaks internal detail: %s", body)
	}
	if !strings.Contains(body, "mcp_a") {
		t.Fatalf("readyz should mention dependency name: %s", body)
	}
}

func TestReadyzChecksEveryDependency(t *testing.T) {
	handler := mustNewServer(t, transport.Services{
		AgentCard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:       http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:      http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		HealthChecks: []platform.HealthChecker{
			&unhealthyChecker{name: "first", detail: "first failure"},
			healthy{},
			&unhealthyChecker{name: "second", detail: "second failure"},
		},
	})
	response := httptest.NewRecorder()
	handler.OpsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	body := response.Body.String()
	if !strings.Contains(body, "first") || !strings.Contains(body, "second") {
		t.Fatalf("readyz omitted a failed dependency: %s", body)
	}
	if strings.Contains(body, "first failure") || strings.Contains(body, "second failure") {
		t.Fatalf("readyz leaks dependency detail: %s", body)
	}
}

func TestReadyzDoesNotInheritClientCancellation(t *testing.T) {
	handler := mustNewServer(t, transport.Services{
		AgentCard:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		HealthChecks: []platform.HealthChecker{contextAwareChecker{}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := httptest.NewRecorder()
	handler.OpsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx))

	if response.Code != http.StatusOK {
		t.Fatalf("canceled readiness status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestReadinessHasIndependentBudget(t *testing.T) {
	handler := mustNewServer(t, transport.Services{
		AgentCard:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		HealthChecks: []platform.HealthChecker{deadlineChecker{}},
	})
	response := httptest.NewRecorder()
	started := time.Now()
	handler.OpsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("readiness elapsed = %v, want less than the 2s healthcheck client timeout", elapsed)
	}
}

func TestReadinessReusesFreshDependencyResults(t *testing.T) {
	checker := &countingChecker{}
	handler := mustNewServer(t, transport.Services{
		AgentCard:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:          http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		HealthChecks: []platform.HealthChecker{checker},
	})
	for attempt := 0; attempt < 3; attempt++ {
		response := httptest.NewRecorder()
		handler.OpsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("readiness attempt %d status = %d", attempt, response.Code)
		}
	}
	if got := checker.count.Load(); got != 1 {
		t.Fatalf("dependency checks within TTL = %d, want 1", got)
	}
}

func TestReadinessCacheCanBeDisabled(t *testing.T) {
	checker := &countingChecker{}
	handler := mustNewServer(t, transport.Services{
		AgentCard:         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:               http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI:              http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		HealthChecks:      []platform.HealthChecker{checker},
		ReadinessCacheTTL: -1,
	})
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		handler.OpsHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("readiness attempt %d status = %d", attempt, response.Code)
		}
	}
	if got := checker.count.Load(); got != 2 {
		t.Fatalf("dependency checks with cache disabled = %d, want 2", got)
	}
}

func TestPprofRequiresServiceToken(t *testing.T) {
	handler := newServer(t, "secret")
	ops := handler.OpsHandler()

	unauthenticated := httptest.NewRecorder()
	ops.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pprof status = %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}

	authenticated := httptest.NewRequest(http.MethodGet, "/debug/pprof/heap?debug=1", nil)
	authenticated.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	ops.ServeHTTP(response, authenticated)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated heap profile status = %d, want %d", response.Code, http.StatusOK)
	}
}

type unhealthyChecker struct {
	name   string
	detail string
}

type contextAwareChecker struct{}

func (contextAwareChecker) CheckHealth(ctx context.Context) platform.DependencyHealth {
	return platform.DependencyHealth{Name: "context", OK: ctx.Err() == nil}
}

type deadlineChecker struct{}

func (deadlineChecker) CheckHealth(ctx context.Context) platform.DependencyHealth {
	<-ctx.Done()
	return platform.DependencyHealth{Name: "deadline", OK: false, Detail: "timeout"}
}

func (c *unhealthyChecker) CheckHealth(context.Context) platform.DependencyHealth {
	return platform.DependencyHealth{Name: c.name, OK: false, Detail: c.detail}
}

func TestIdentityHeaderOnlyAcceptedOnAGUI(t *testing.T) {
	var a2aID, aguiID identity.Identity
	services := transport.Services{
		AgentCard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, ok := identity.FromContext(r.Context()); ok {
				a2aID = id
			}
			w.WriteHeader(http.StatusAccepted)
		}),
		AGUI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, ok := identity.FromContext(r.Context()); ok {
				aguiID = id
			}
			w.WriteHeader(http.StatusAccepted)
		}),
	}
	server := mustNewServer(t, services)

	// A2A must not trust the browser-identity header injected for AG-UI.
	req := httptest.NewRequest(http.MethodPost, "/a2a", nil)
	req.Header.Set("X-Agent-User-ID", "alice")
	server.ServeHTTP(httptest.NewRecorder(), req)
	if !a2aID.IsZero() {
		t.Fatalf("a2a identity = %+v, want zero (shared-token caller must not spoof users)", a2aID)
	}

	// AG-UI adopts the verified identity from the header.
	req2 := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	req2.Header.Set("X-Agent-User-ID", "alice")
	server.ServeHTTP(httptest.NewRecorder(), req2)
	if aguiID.IsZero() || aguiID.UserID != "alice" || aguiID.MutualTLS {
		t.Fatalf("ag-ui identity = %+v, want alice without the mutual-TLS mark", aguiID)
	}
}

func TestMutualTLSRequestsTrustRemoteUserAndSkipToken(t *testing.T) {
	var aguiID identity.Identity
	server := mustNewServer(t, transport.Services{
		AgentCard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		A2A:       http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }),
		AGUI: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id, ok := identity.FromContext(r.Context()); ok {
				aguiID = id
			}
			w.WriteHeader(http.StatusAccepted)
		}),
		InternalToken: "secret",
	})

	request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(`{}`))
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	request.Header.Set("Remote-User", "alice")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("mTLS status = %d, want %d (client certificate replaces the service token)", response.Code, http.StatusAccepted)
	}
	if aguiID.UserID != "alice" || !aguiID.MutualTLS {
		t.Fatalf("mTLS identity = %+v, want alice from Remote-User over the mutually authenticated channel", aguiID)
	}
}

func TestPlainRequestsIgnoreRemoteUserAndLegacyHeaderOnMutualTLS(t *testing.T) {
	server := newServer(t, "")
	request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(`{}`))
	request.Header.Set("Remote-User", "spoofed")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("plain Remote-User status = %d, want 401 (only mTLS may inject it)", response.Code)
	}

	// mTLS 通道不信任旧的中继头（X-Agent-User-ID 只属于带 token 的明文中继）。
	legacy := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(`{}`))
	legacy.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	legacy.Header.Set("X-Agent-User-ID", "alice")
	legacyResponse := httptest.NewRecorder()
	server.ServeHTTP(legacyResponse, legacy)
	if legacyResponse.Code != http.StatusUnauthorized {
		t.Fatalf("mTLS legacy-header status = %d, want 401", legacyResponse.Code)
	}
}

func TestErrorResponsesUseRFC9457ProblemJSON(t *testing.T) {
	server := newServer(t, "s3cr3t")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/a2a", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("content type = %q, want problem+json", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("body lacks stable error code: %s", rec.Body.String())
	}
}
