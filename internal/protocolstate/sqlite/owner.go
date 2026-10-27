package sqlite

import (
	"context"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/ml8s/liki-agent-runtime/internal/platform/identity"
)

// principal is the durable ownership scope. A2A tenant plus authenticated
// caller forms the isolation boundary; the composite key prevents a tenant ID
// from colliding with a differently scoped caller name.
type principal struct {
	Tenant   string
	User     string
	ownerKey string
}

func ownerKey(tenant, user string) string {
	return fmt.Sprintf("%s\x00%s", tenant, user)
}

func principalFromContext(ctx context.Context) (principal, error) {
	if verified, ok := identity.FromContext(ctx); ok {
		result := principal{Tenant: verified.Tenant, User: verified.UserID}
		result.ownerKey = ownerKey(result.Tenant, result.User)
		return result, nil
	}
	callCtx, ok := a2asrv.CallContextFrom(ctx)
	if !ok || callCtx.User == nil || !callCtx.User.Authenticated || callCtx.User.Name == "" {
		return principal{}, a2a.ErrUnauthenticated
	}
	result := principal{
		Tenant: callCtx.Tenant(),
		User:   callCtx.User.Name,
	}
	result.ownerKey = ownerKey(result.Tenant, result.User)
	return result, nil
}
