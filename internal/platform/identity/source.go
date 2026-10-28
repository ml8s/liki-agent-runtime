package identity

import (
	"net/http"

	"github.com/ml8s/agent-runtime/internal/domain"
)

// RequestSource is the transport port for a verified end-user identity. It is
// intentionally separate from service authentication: a service token proves
// the caller; an identity source proves the end user acting through that
// caller.
type RequestSource interface {
	IdentityFromRequest(request *http.Request) (Identity, bool)
}

const (
	// PlainUserHeader is used by an authenticated BFF over the internal
	// bearer-token channel.
	PlainUserHeader = "X-Agent-User-ID"
	// PlainTenantHeader is the tenant counterpart to PlainUserHeader.
	PlainTenantHeader = "X-Agent-Tenant-ID"
	// TLSUserHeader is injected only after the listener verifies a client
	// certificate.
	TLSUserHeader = "Remote-User"
	// TLSTenantHeader is the tenant counterpart to TLSUserHeader.
	TLSTenantHeader = "Remote-Tenant"
)

// HeaderSourceConfig names the headers owned by each trusted channel.
type HeaderSourceConfig struct {
	PlainUserHeader   string
	PlainTenantHeader string
	TLSUserHeader     string
	TLSTenantHeader   string
}

func (config HeaderSourceConfig) withDefaults() HeaderSourceConfig {
	if config.PlainUserHeader == "" {
		config.PlainUserHeader = PlainUserHeader
	}
	if config.PlainTenantHeader == "" {
		config.PlainTenantHeader = PlainTenantHeader
	}
	if config.TLSUserHeader == "" {
		config.TLSUserHeader = TLSUserHeader
	}
	if config.TLSTenantHeader == "" {
		config.TLSTenantHeader = TLSTenantHeader
	}
	return config
}

// HeaderSource maps channel-authenticated trusted gateway headers to the
// runtime identity context. mTLS headers are consulted only after the HTTP
// listener has verified a client certificate; relay headers are consulted on
// the bearer-token channel.
type HeaderSource struct {
	config HeaderSourceConfig
}

// NewHeaderSource creates the trusted-gateway identity adapter.
func NewHeaderSource(config HeaderSourceConfig) HeaderSource {
	return HeaderSource{config: config.withDefaults()}
}

// IdentityFromRequest implements RequestSource.
func (source HeaderSource) IdentityFromRequest(request *http.Request) (Identity, bool) {
	if request == nil {
		return Identity{}, false
	}
	userHeader := source.config.PlainUserHeader
	tenantHeader := source.config.PlainTenantHeader
	mutualTLS := MutualTLSVerified(request)
	if mutualTLS {
		userHeader = source.config.TLSUserHeader
		tenantHeader = source.config.TLSTenantHeader
	}

	userID := trimHeader(request.Header.Get(userHeader))
	if !domain.ValidIdentifier(userID) {
		return Identity{}, false
	}
	tenant := trimHeader(request.Header.Get(tenantHeader))
	if tenant != "" && !domain.ValidIdentifier(tenant) {
		return Identity{}, false
	}
	return Identity{UserID: userID, Tenant: tenant, MutualTLS: mutualTLS}, true
}

// MutualTLSVerified reports whether the listener already verified a client
// certificate for this request.
func MutualTLSVerified(request *http.Request) bool {
	return request != nil && request.TLS != nil && len(request.TLS.PeerCertificates) > 0
}

func trimHeader(value string) string {
	if len(value) == 0 {
		return value
	}
	start := 0
	for start < len(value) && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	end := len(value)
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}
