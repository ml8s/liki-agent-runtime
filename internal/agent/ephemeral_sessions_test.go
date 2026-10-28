package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/execution"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/session"
)

// recordingSessionService records which session IDs pass through each method
// and delegates to a real service, so tests can observe storage selection
// without changing behavior.
type recordingSessionService struct {
	inner session.Service

	mu       sync.Mutex
	created  []string
	appended []string
	listed   bool
	deleted  []string
}

func (s *recordingSessionService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	response, err := s.inner.Create(ctx, req)
	if err == nil {
		s.mu.Lock()
		s.created = append(s.created, req.SessionID)
		s.mu.Unlock()
	}
	return response, err
}

func (s *recordingSessionService) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	return s.inner.Get(ctx, req)
}

func (s *recordingSessionService) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	response, err := s.inner.List(ctx, req)
	if err == nil {
		s.mu.Lock()
		s.listed = true
		s.mu.Unlock()
	}
	return response, err
}

func (s *recordingSessionService) Delete(ctx context.Context, req *session.DeleteRequest) error {
	err := s.inner.Delete(ctx, req)
	if err == nil {
		s.mu.Lock()
		s.deleted = append(s.deleted, req.SessionID)
		s.mu.Unlock()
	}
	return err
}

func (s *recordingSessionService) AppendEvent(ctx context.Context, current session.Session, event *session.Event) error {
	err := s.inner.AppendEvent(ctx, current, event)
	if err == nil {
		s.mu.Lock()
		s.appended = append(s.appended, current.ID())
		s.mu.Unlock()
	}
	return err
}

func (s *recordingSessionService) snapshot() (created, appended, deleted []string, listed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.created...), append([]string(nil), s.appended...),
		append([]string(nil), s.deleted...), s.listed
}

func newEphemeralTestRuntime(
	t *testing.T,
	agentDeployment *deploymentpkg.AgentDeployment,
	durable, ephemeral *recordingSessionService,
) *Runtime {
	t.Helper()
	events := &recordingAudit{}
	clientTransport := connectStubMCPServer(t)
	runtime, err := NewRuntime(Config{
		Model:                           "fake-model",
		AgentDeployment:                 agentDeployment,
		AuditRecorder:                   events,
		modelOverride:                   &cyclingLLM{turns: cyclingTurns()},
		SessionService:                  durable,
		ephemeralSessionServiceOverride: ephemeral,
		mcpTransportOverrides:           map[string]mcp.Transport{"test": clientTransport},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func ephemeralRunRequest(t *testing.T, runID string) execution.RunRequest {
	t.Helper()
	return execution.RunRequest{
		RunID:       runID,
		ThreadID:    "thread_" + runID,
		UserID:      "user_1",
		UserMessage: "当前问题",
		History:     benchHistory(),
	}
}

// TestRunUsesEphemeralSessionStore pins the storage split: a Runtime.Run with
// caller history must live entirely in the ephemeral in-process store and
// never touch the durable protocol session store.
func TestRunUsesEphemeralSessionStore(t *testing.T) {
	durable := &recordingSessionService{inner: session.InMemoryService()}
	ephemeral := &recordingSessionService{inner: session.InMemoryService()}
	runtime := newEphemeralTestRuntime(t, plainTestDeployment(t), durable, ephemeral)

	if _, err := runtime.Run(context.Background(), ephemeralRunRequest(t, "run_ephemeral_1"), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	created, appended, deleted, _ := ephemeral.snapshot()
	if len(created) != 1 || !strings.HasPrefix(created[0], "run:run_ephemeral_1") {
		t.Fatalf("ephemeral created sessions = %v, want one run-scoped session", created)
	}
	if len(appended) == 0 {
		t.Fatal("ephemeral store saw no history appends")
	}
	if len(deleted) != 1 || deleted[0] != created[0] {
		t.Fatalf("ephemeral deleted sessions = %v, want terminal deletion of %v", deleted, created)
	}

	durableCreated, durableAppended, durableDeleted, _ := durable.snapshot()
	if len(durableCreated)+len(durableAppended)+len(durableDeleted) != 0 {
		t.Fatalf("durable store observed run-scoped sessions: created=%v appended=%v deleted=%v",
			durableCreated, durableAppended, durableDeleted)
	}
}

// TestRunnerConfigKeepsDurableSessionService pins that protocol adapters
// (the A2A executor) keep receiving the durable session service: ContextID-
// owned sessions must remain persistent across attempts.
func TestRunnerConfigKeepsDurableSessionService(t *testing.T) {
	durable := &recordingSessionService{inner: session.InMemoryService()}
	ephemeral := &recordingSessionService{inner: session.InMemoryService()}
	runtime := newEphemeralTestRuntime(t, plainTestDeployment(t), durable, ephemeral)

	if runtime.RunnerConfig().SessionService != session.Service(durable) {
		t.Fatal("RunnerConfig() does not carry the durable session service")
	}
}

// TestRetainIdleSessionsSweepsOnlyDurableStore pins that the retention sweep
// covers only durable protocol sessions; ephemeral run sessions are
// process-local and never enter it.
func TestRetainIdleSessionsSweepsOnlyDurableStore(t *testing.T) {
	durable := &recordingSessionService{inner: session.InMemoryService()}
	ephemeral := &recordingSessionService{inner: session.InMemoryService()}
	runtime := newEphemeralTestRuntime(t, plainTestDeployment(t), durable, ephemeral)

	if _, err := runtime.RetainIdleSessions(context.Background(), time.Hour, time.Now().UTC(), nil); err != nil {
		t.Fatalf("RetainIdleSessions() error = %v", err)
	}
	_, _, _, durableListed := durable.snapshot()
	_, _, _, ephemeralListed := ephemeral.snapshot()
	if !durableListed {
		t.Fatal("retention sweep skipped the durable session store")
	}
	if ephemeralListed {
		t.Fatal("retention sweep touched the ephemeral session store")
	}
}
