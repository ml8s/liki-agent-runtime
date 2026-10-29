package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/platform/identity"
)

func TestA2AServiceIdentityPropagation(t *testing.T) {
	authenticated := false
	server := &Server{services: Services{
		ProtocolToken: "token",
		A2APrincipal:  "svc-a2a",
		A2ATenant:     "tenant-a",
	}, authFailures: newAuthFailureLimiter(), readinessCache: &readinessCache{ttl: time.Minute}}
	server.readiness.Store(true)
	server.routes = http.NewServeMux()
	server.routes.Handle("POST /a2a", server.authorized(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		authenticated = true
		verified, ok := identity.FromContext(r.Context())
		if !ok || verified.UserID != "svc-a2a" || verified.Tenant != "tenant-a" {
			t.Fatalf("identity = %#v, want configured A2A principal", verified)
		}
	}), false))
	request := httptest.NewRequest(http.MethodPost, "/a2a", nil)
	request.Header.Set("Authorization", "Bearer token")
	server.ServeHTTP(httptest.NewRecorder(), request)
	if !authenticated {
		t.Fatal("authenticated handler was not reached")
	}
}
