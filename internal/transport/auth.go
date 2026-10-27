package transport

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"github.com/ml8s/liki-agent-runtime/internal/platform/identity"
)

const (
	authFailureLimit      = 10
	authFailureWindow     = time.Minute
	maxAuthFailureSources = 10000
)

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

func (s *Server) authorized(next http.Handler, requireIdentity bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source := requestSource(r.RemoteAddr)
		// An mTLS channel (client certificate already verified) uses the
		// certificate as the channel credential; a plaintext listener
		// (transitional relay) still requires the service token.
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

func verifiedIdentity(r *http.Request) identity.Identity {
	// The channel determines the trusted identity header (contract C4):
	//   mTLS (edge forward_auth injection) → Remote-User;
	//   plaintext (token-bearing relay caller) → X-Liki-User-ID.
	mtls := mutualTLSVerified(r)
	header := "X-Liki-User-ID"
	tenantHeader := "X-Liki-Tenant-ID"
	if mtls {
		header = "Remote-User"
		tenantHeader = "Remote-Tenant"
	}
	userID := strings.TrimSpace(r.Header.Get(header))
	if !validRequestID(userID) {
		return identity.Identity{}
	}
	tenant := strings.TrimSpace(r.Header.Get(tenantHeader))
	if tenant != "" && !validRequestID(tenant) {
		return identity.Identity{}
	}
	return identity.Identity{UserID: userID, Tenant: tenant, MutualTLS: mtls}
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
