package agent

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/adk/v2/session"
)

// SessionRetentionResult reports one bounded idle-session sweep.
type SessionRetentionResult struct {
	Scanned        int64
	SkippedUnknown int64
	Deleted        int64
	DeleteFailures int64
}

// RetainIdleSessions removes runtime-owned ADK sessions whose official
// LastUpdateTime is older than retention. The caller must explicitly exclude
// protocol sessions that still own non-terminal work. Sessions with an unknown
// timestamp are preserved because their age cannot be established safely.
func (r *Runtime) RetainIdleSessions(
	ctx context.Context,
	retention time.Duration,
	now time.Time,
	protectedSessionIDs map[string]struct{},
) (SessionRetentionResult, error) {
	if r == nil || r.sessions == nil {
		return SessionRetentionResult{}, fmt.Errorf("session service is unavailable")
	}
	if retention <= 0 {
		return SessionRetentionResult{}, fmt.Errorf("retention must be greater than zero")
	}
	if now.IsZero() {
		return SessionRetentionResult{}, fmt.Errorf("retention now is required")
	}
	response, err := r.sessions.List(ctx, &session.ListRequest{AppName: r.config.AppName})
	if err != nil {
		return SessionRetentionResult{}, fmt.Errorf("list sessions for retention: %w", err)
	}

	result := SessionRetentionResult{}
	for _, candidate := range response.Sessions {
		if candidate == nil {
			continue
		}
		result.Scanned++
		sessionID := candidate.ID()
		if _, protected := protectedSessionIDs[sessionID]; protected {
			continue
		}
		lastUpdate := candidate.LastUpdateTime()
		if lastUpdate.IsZero() {
			result.SkippedUnknown++
			continue
		}
		if now.Sub(lastUpdate) < retention {
			continue
		}
		if err := r.sessions.Delete(ctx, &session.DeleteRequest{
			AppName:   candidate.AppName(),
			UserID:    candidate.UserID(),
			SessionID: sessionID,
		}); err != nil {
			result.DeleteFailures++
			r.config.Logger.WarnContext(ctx, "adk_session_retention_delete_failed",
				"session_id", sessionID,
				"error", runtimeError(err).Error(),
			)
			continue
		}
		result.Deleted++
	}
	return result, nil
}
