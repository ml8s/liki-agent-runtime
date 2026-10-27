package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/sessiontestsuite"
)

func TestSQLiteSessionServicePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	service, err := Open(path)
	if err != nil {
		t.Fatalf("OpenSQLiteSessionService() error = %v", err)
	}
	created, err := service.SessionService().Create(context.Background(), &session.CreateRequest{
		AppName:   "agent-runtime",
		UserID:    "user-1",
		SessionID: "context-1",
		State:     map[string]any{"attempt": "attempt-1"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen OpenSQLiteSessionService() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.SessionService().Get(context.Background(), &session.GetRequest{
		AppName:   "agent-runtime",
		UserID:    "user-1",
		SessionID: "context-1",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Session == nil || got.Session.ID() != created.Session.ID() {
		t.Fatalf("Get() = %#v, want persisted session", got.Session)
	}
	if value, err := got.Session.State().Get("attempt"); err != nil || value != "attempt-1" {
		t.Fatalf("State().Get(attempt) = %#v/%v, want attempt-1", value, err)
	}
}

func TestSQLiteSessionServiceOfficialSuite(t *testing.T) {
	sessiontestsuite.RunServiceTests(t, sessiontestsuite.SuiteOptions{
		SupportsUserProvidedSessionID: true,
	}, func(t *testing.T) session.Service {
		service, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
		if err != nil {
			t.Fatalf("OpenSQLiteSessionService() error = %v", err)
		}
		t.Cleanup(func() { _ = service.Close() })
		return service.SessionService()
	})
}
