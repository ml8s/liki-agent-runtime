package agent

import (
	"context"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"
)

func appendSessionAt(t *testing.T, runtime *Runtime, userID, sessionID string, at time.Time) {
	t.Helper()
	response, err := runtime.sessions.Create(context.Background(), &session.CreateRequest{
		AppName:   runtime.config.AppName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("Create(%s) error = %v", sessionID, err)
	}
	if err := runtime.sessions.AppendEvent(context.Background(), response.Session, &session.Event{
		ID:        sessionID + "-event",
		Timestamp: at,
		Author:    "user",
	}); err != nil {
		t.Fatalf("AppendEvent(%s) error = %v", sessionID, err)
	}
}

func sessionExists(t *testing.T, runtime *Runtime, userID, sessionID string) bool {
	t.Helper()
	response, err := runtime.sessions.Get(context.Background(), &session.GetRequest{
		AppName:   runtime.config.AppName,
		UserID:    userID,
		SessionID: sessionID,
	})
	return err == nil && response != nil && response.Session != nil
}

func TestRetainIdleSessionsKeepsProtectedAndUnknownAgeSessions(t *testing.T) {
	runtime := newTestRuntime(t, plainTestDeployment(t), &recordingAudit{}, &fakeLLM{})
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	appendSessionAt(t, runtime, "user_1", "expired", now.Add(-2*time.Hour))
	appendSessionAt(t, runtime, "user_1", "protected", now.Add(-2*time.Hour))
	appendSessionAt(t, runtime, "user_1", "recent", now.Add(-time.Minute))

	result, err := runtime.RetainIdleSessions(context.Background(), time.Hour, now, map[string]struct{}{
		"protected": {},
	})
	if err != nil {
		t.Fatalf("RetainIdleSessions() error = %v", err)
	}
	if result.Scanned != 3 || result.Deleted != 1 || result.SkippedUnknown != 0 || result.DeleteFailures != 0 {
		t.Fatalf("retention result = %+v, want three scanned and one deleted", result)
	}
	if sessionExists(t, runtime, "user_1", "expired") {
		t.Fatal("expired session survived")
	}
	if !sessionExists(t, runtime, "user_1", "protected") {
		t.Fatal("protected session was deleted")
	}
	if !sessionExists(t, runtime, "user_1", "recent") {
		t.Fatal("recent session was deleted")
	}
}

func TestRetainIdleSessionsFailsClosed(t *testing.T) {
	runtime := newTestRuntime(t, plainTestDeployment(t), &recordingAudit{}, &fakeLLM{})
	if _, err := runtime.RetainIdleSessions(context.Background(), 0, time.Now(), nil); err == nil {
		t.Fatal("zero retention was accepted")
	}
	if _, err := runtime.RetainIdleSessions(context.Background(), time.Hour, time.Time{}, nil); err == nil {
		t.Fatal("missing now was accepted")
	}
}

func TestRetainIdleSessionsRejectsInvalidService(t *testing.T) {
	var runtime *Runtime
	if _, err := runtime.RetainIdleSessions(context.Background(), time.Hour, time.Now(), nil); err == nil {
		t.Fatal("nil runtime was accepted")
	}
	runtime = &Runtime{}
	if _, err := runtime.RetainIdleSessions(context.Background(), time.Hour, time.Now(), nil); err == nil {
		t.Fatal("missing session service was accepted")
	}
}
