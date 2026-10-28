package agent

import (
	"context"
	"iter"
	"sync"
	"testing"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/ml8s/agent-runtime/internal/testagent"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ml8s/agent-runtime/internal/audit"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// testDeployment delegates to the shared testagent helper so the agent and
// protocol test suites exercise the same deployment fixture.
func testDeployment(t testing.TB) *deploymentpkg.AgentDeployment {
	t.Helper()
	return testagent.AgentDeployment(t)
}

type fakeLLM struct {
	responses []*model.LLMResponse
	err       error
}

func (f *fakeLLM) Name() string { return "fake-model" }

func (f *fakeLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, response := range f.responses {
			if !yield(response, nil) {
				return
			}
		}
		if f.err != nil {
			yield(nil, f.err)
		}
	}
}

type blockingLLM struct {
	started   chan struct{}
	unblocked chan struct{}
}

func (f *blockingLLM) Name() string { return "fake-model" }

func (f *blockingLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		close(f.started)
		<-f.unblocked
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"ok"}`}}},
		}, nil)
	}
}

// turnBasedLLM yields one response per model invocation. fakeLLM models chunks
// within one invocation; tool loops need distinct invocation turns.
type turnBasedLLM struct {
	mu    sync.Mutex
	turn  int
	turns []*model.LLMResponse
}

func (f *turnBasedLLM) Name() string { return "fake-model" }

func (f *turnBasedLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		f.mu.Lock()
		index := f.turn
		f.turn++
		response := (*model.LLMResponse)(nil)
		if index < len(f.turns) {
			response = f.turns[index]
		}
		f.mu.Unlock()
		if response != nil {
			yield(response, nil)
		}
	}
}

type recordingAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recordingAudit) Record(_ context.Context, event *audit.Event) error {
	if event == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *event)
	return nil
}

func (r *recordingAudit) eventsOfType(eventType audit.EventType) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]audit.Event, 0)
	for _, event := range r.events {
		if event.Type == eventType {
			result = append(result, event)
		}
	}
	return result
}

func (r *recordingAudit) lastEvent() audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[len(r.events)-1]
}

// newTestRuntime wires a fake model and an in-memory MCP server. This avoids
// network dependencies while retaining ADK's real runner and toolset behavior.
func newTestRuntime(t testing.TB, agentDeployment *deploymentpkg.AgentDeployment, events *recordingAudit, llm model.LLM) *Runtime {
	return newTestRuntimeWithLimit(t, agentDeployment, events, llm, 0)
}

func newTestRuntimeWithLimit(
	t testing.TB,
	agentDeployment *deploymentpkg.AgentDeployment, events *recordingAudit,
	llm model.LLM,
	maxRuns int,
) *Runtime {
	t.Helper()
	clientTransport := connectStubMCPServer(t)
	runtime, err := NewRuntime(Config{
		Model:                 "fake-model",
		AgentDeployment:       agentDeployment,
		AuditRecorder:         events,
		MaxConcurrentRuns:     maxRuns,
		modelOverride:         llm,
		mcpTransportOverrides: map[string]mcp.Transport{"test": clientTransport},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

// newTestRuntimeWithSessions wires the same fake model and MCP server but
// injects an explicit durable session service, mirroring the production
// composition where protocol sessions are persisted.
func newTestRuntimeWithSessions(
	t testing.TB,
	agentDeployment *deploymentpkg.AgentDeployment, events *recordingAudit,
	llm model.LLM, sessions session.Service,
) *Runtime {
	t.Helper()
	clientTransport := connectStubMCPServer(t)
	runtime, err := NewRuntime(Config{
		Model:                 "fake-model",
		AgentDeployment:       agentDeployment,
		AuditRecorder:         events,
		modelOverride:         llm,
		SessionService:        sessions,
		mcpTransportOverrides: map[string]mcp.Transport{"test": clientTransport},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

// connectStubMCPServer starts one deterministic in-memory MCP server for the
// runtime's "test" logical server and returns the client transport.
func connectStubMCPServer(t testing.TB) mcp.Transport {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "stub-mcp", Version: "test"}, nil)
	server.AddTool(
		&mcp.Tool{Name: "test_tool", InputSchema: &jsonschema.Schema{Type: "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: map[string]any{"answer": "tool-output"}}, nil
		},
	)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect test MCP server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	return clientTransport
}

func plainTestDeployment(t testing.TB) *deploymentpkg.AgentDeployment {
	t.Helper()
	agentDeployment := NewTestDeployment(t)
	for index := range agentDeployment.Spec.Agents {
		agentDeployment.Spec.Agents[index].Output = deploymentpkg.OutputDefinition{}
		agentDeployment.Spec.Agents[index].OutputSchema = nil
		agentDeployment.Spec.Agents[index].ResolvedOutput = nil
		agentDeployment.Spec.Agents[index].SchemaDigest = ""
	}
	if err := agentDeployment.Validate(); err != nil {
		t.Fatalf("validate plain agentDeployment: %v", err)
	}
	return agentDeployment
}

// NewTestDeployment exposes the generic test agentDeployment to external adapter
// tests compiled into the same test binary.
func NewTestDeployment(t testing.TB) *deploymentpkg.AgentDeployment {
	t.Setenv("TEST_MCP_ENDPOINT", "in-memory://test")
	return testDeployment(t)
}
