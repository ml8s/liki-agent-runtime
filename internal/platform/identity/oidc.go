package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/ml8s/agent-runtime/internal/domain"
)

// CallerAuthenticator is implemented by identity sources whose credential also
// authenticates the caller. Unlike trusted-header mode, a verified OIDC
// identity is accepted on both browser and machine protocol surfaces.
type CallerAuthenticator interface {
	AuthenticatesCaller() bool
}

// OIDCConfig configures standards-based bearer identity verification.
type OIDCConfig struct {
	Issuer        string
	Audience      string
	UsernameClaim string
	TenantClaim   string
}

// OIDCSource verifies a standard `Authorization: Bearer <id-token>` credential
// with an OIDC provider. Discovery, key lookup, signature, issuer, audience,
// and expiry verification are delegated to the OIDC library; this adapter maps
// verified claims to the runtime identity.
type OIDCSource struct {
	verifier      *oidc.IDTokenVerifier
	usernameClaim string
	tenantClaim   string
}

// NewOIDCSource performs OIDC discovery and returns a request source backed by
// the provider's remote JWKS.
func NewOIDCSource(ctx context.Context, config OIDCConfig) (*OIDCSource, error) {
	config.UsernameClaim = strings.TrimSpace(config.UsernameClaim)
	if strings.TrimSpace(config.Issuer) == "" {
		return nil, fmt.Errorf("OIDC issuer is required")
	}
	if strings.TrimSpace(config.Audience) == "" {
		return nil, fmt.Errorf("OIDC audience is required")
	}
	if config.UsernameClaim == "" {
		config.UsernameClaim = "sub"
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryCtx, config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	verifier := provider.VerifierContext(context.Background(), &oidc.Config{
		ClientID: config.Audience,
	})
	return &OIDCSource{
		verifier:      verifier,
		usernameClaim: config.UsernameClaim,
		tenantClaim:   strings.TrimSpace(config.TenantClaim),
	}, nil
}

// AuthenticatesCaller implements CallerAuthenticator.
func (source *OIDCSource) AuthenticatesCaller() bool {
	return source != nil && source.verifier != nil
}

// IdentityFromRequest implements RequestSource.
func (source *OIDCSource) IdentityFromRequest(request *http.Request) (Identity, bool) {
	if request == nil || source == nil || source.verifier == nil {
		return Identity{}, false
	}
	rawToken, ok := bearerToken(request)
	if !ok {
		return Identity{}, false
	}
	token, err := source.verifier.Verify(request.Context(), rawToken)
	if err != nil {
		return Identity{}, false
	}
	var claims map[string]any
	if err := token.Claims(&claims); err != nil {
		return Identity{}, false
	}
	userID, ok := stringClaim(claims, source.usernameClaim)
	if !ok || !domain.ValidIdentifier(userID) {
		return Identity{}, false
	}
	tenant, _ := stringClaim(claims, source.tenantClaim)
	if tenant != "" && !domain.ValidIdentifier(tenant) {
		return Identity{}, false
	}
	return Identity{UserID: userID, Tenant: tenant}, true
}

func bearerToken(request *http.Request) (string, bool) {
	const prefix = "Bearer "
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

func stringClaim(claims map[string]any, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	value, ok := claims[name]
	if !ok || value == nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		value = strings.TrimSpace(typed)
	default:
		return "", false
	}
	text, _ := value.(string)
	return text, text != ""
}
