// Package identity carries the verified caller identity across service
// boundaries. liki-web authenticates the end user; liki-agent-runtime trusts this
// kernel identity only after its own service authentication.
package identity

import (
	"context"
	"strings"
)

// Identity is the verified caller identity propagated across service
// boundaries.
type Identity struct {
	UserID string
	// MutualTLS reports that the request arrived over the mutually
	// authenticated listener. Callbacks and other direct-connect-only
	// behaviours key off this channel fact, never off header contents.
	MutualTLS bool
}

// IsZero reports whether the identity carries no verified subject.
func (i Identity) IsZero() bool {
	return strings.TrimSpace(i.UserID) == ""
}

type contextKey struct{}

// WithIdentity returns a context carrying the verified identity.
func WithIdentity(ctx context.Context, subject Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, subject)
}

// FromContext returns the verified identity, if present.
func FromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	if !ok || identity.IsZero() {
		return Identity{}, false
	}
	return identity, true
}

// UserIDFromContext returns the verified user id, if present.
func UserIDFromContext(ctx context.Context) (string, bool) {
	identity, ok := FromContext(ctx)
	if !ok {
		return "", false
	}
	return identity.UserID, true
}
