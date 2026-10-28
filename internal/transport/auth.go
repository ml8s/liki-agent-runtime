package transport

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ml8s/agent-runtime/internal/domain"
	"github.com/ml8s/agent-runtime/internal/platform/identity"
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

// bearerCredential returns the token following a case-insensitive Bearer
// authorization scheme, as required by RFC 7235.
func bearerCredential(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// bearerMatches reports whether the header carries exactly the expected
// bearer credential. Comparison is constant-time so tokens cannot be probed
// byte by byte.
func bearerMatches(header, expected string) bool {
	token, ok := bearerCredential(header)
	return ok && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (s *Server) authorized(next http.Handler, requireIdentity bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source := requestSource(r.RemoteAddr)
		// An mTLS channel (client certificate already verified) uses the
		// certificate as the channel credential; a plaintext listener
		// (transitional relay) still requires the service token.
		if callerAuthenticatedSource(s.services.IdentitySource) {
			if _, ok := identity.FromContext(r.Context()); !ok {
				if !s.authFailures.tryConsume(source, time.Now()) {
					w.Header().Set("Retry-After", "60")
					writeError(w, http.StatusTooManyRequests, domain.CodeRateLimited, "too many authentication failures")
					return
				}
				writeError(w, http.StatusUnauthorized, domain.CodeUnauthorized, "valid OIDC bearer token required")
				return
			}
		} else if s.services.InternalToken != "" && !identity.MutualTLSVerified(r) {
			header := r.Header.Get("Authorization")
			if !bearerMatches(header, s.services.InternalToken) {
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

// authorizedOps gates the operational surface. A dedicated ops token, when
// configured, is the only accepted credential there: metric scrapers and
// profilers must not hold the protocol service token. Without it, the
// composed protocol authentication policy continues to own the surface.
func (s *Server) authorizedOps(next http.Handler) http.Handler {
	if s.services.OpsToken == "" {
		return s.authorized(next, false)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source := requestSource(r.RemoteAddr)
		if !bearerMatches(r.Header.Get("Authorization"), s.services.OpsToken) {
			if !s.authFailures.tryConsume(source, time.Now()) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, domain.CodeRateLimited, "too many authentication failures")
				return
			}
			writeError(w, http.StatusUnauthorized, domain.CodeUnauthorized, "valid ops token required")
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

func validRequestID(value string) bool {
	return domain.ValidIdentifier(value)
}
