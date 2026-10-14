package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func buildTestDeployment(t testing.TB) (*Deployment, error) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"agent-deployment.json": `{
			"schema_version": "liki.agent-deployment.v1", "metadata": {"name": "test-agent", "version": "1.0.0"},
			"spec": {
				"mcpServers": [{"name": "test", "endpointEnv": "TEST_MCP_ENDPOINT"}],
				"agents": [{
					"name": "main",
					"version": "1.0.0",
					"description": "generic test agent",
					"mode": "chat",
					"sub_agents": [],
					"instruction": {"path": "instruction.md"},
					"output": {
						"schema": {"path": "output.schema.json"},
						"textPointer": "/answer"
					},
					"skills": {"root": "SKILLS_ROOT_PLACEHOLDER"},
									"tools": {"allow": {"test": ["test_tool"], "skilltoolset": ["list_skills", "load_skill", "load_skill_resource"]}}
									}]
			}
		}`,
		"instruction.md": "You are a generic test agent.",
		"output.schema.json": `{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type": "object",
			"additionalProperties": false,
			"required": ["answer"],
			"properties": {
				"answer": {"type": "string", "minLength": 1},
				"confidence": {"type": "number", "minimum": 0, "maximum": 1}
			}
		}`,
	}
	skillRoot := filepath.Join(root, "skills-root")
	writeMinimalSkillTree(t, skillRoot)
	files["agent-deployment.json"] = strings.ReplaceAll(
		files["agent-deployment.json"], "SKILLS_ROOT_PLACEHOLDER", skillRoot)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			return nil, err
		}
	}
	return LoadAgentDeployment(filepath.Join(root, "agent-deployment.json"))
}

func testDeployment(t testing.TB) (*Deployment, error) {
	t.Helper()
	return buildTestDeployment(t)
}

func TestRunStateRejectsInvalidStructuredOutput(t *testing.T) {
	state := &runState{}
	deployment := plainTestDeployment(t)
	entrypoint, err := deployment.EntrypointDefinition()
	if err != nil {
		t.Fatal(err)
	}
	err = state.consumeStructuredOutput(map[string]any{"unexpected": true}, entrypoint)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStructuredOutputInvalid {
		t.Fatalf("consumeStructuredOutput() error = %v, want %s", err, domain.CodeStructuredOutputInvalid)
	}
}

func TestEventProjectorFiltersObserverFacts(t *testing.T) {
	toolCall := &genai.Part{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "test_tool"}}
	testCases := []struct {
		name    string
		event   *session.Event
		wantNil bool
	}{
		{name: "partial payload", event: &session.Event{
			LLMResponse: model.LLMResponse{Partial: true, Content: &genai.Content{Parts: []*genai.Part{{Text: `{"answer":"par`}}}},
		}, wantNil: true},
		{name: "partial tool call", event: &session.Event{
			LLMResponse: model.LLMResponse{Partial: true, Content: &genai.Content{Parts: []*genai.Part{toolCall}}},
		}, wantNil: true},
		{name: "final payload with tool fact", event: &session.Event{
			LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: `{"answer":"raw"}`}, toolCall}}},
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			projector := newEventProjector(NewTestDeployment(t))
			visible, visibleOK := projector.Project(testCase.event)
			if testCase.wantNil {
				if visibleOK {
					t.Fatalf("visible event = %+v, want none", visible)
				}
				return
			}
			if !visibleOK || visible == nil || len(visible.Content.Parts) != 1 || visible.Content.Parts[0].FunctionCall == nil {
				t.Fatalf("visible event = %+v/%v, want one function call", visible, visibleOK)
			}
		})
	}
}

func TestRunStateCapturesDelegatedPlainFinalAnswer(t *testing.T) {
	state := &runState{capturePlain: true}
	event := &session.Event{
		Author: "bazi",
		LLMResponse: model.LLMResponse{
			TurnComplete: true,
			Content:      genai.NewContentFromText("delegated answer", genai.RoleModel),
		},
	}
	state.consume(event)
	if state.output.Text != "delegated answer" {
		t.Fatalf("output text = %q", state.output.Text)
	}
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

// newTestRuntime wires a fake model and an in-memory MCP server. This avoids
// network dependencies while retaining ADK's real runner and toolset behavior.
func newTestRuntime(t *testing.T, deployment *Deployment, events *recordingAudit, llm model.LLM) *Runtime {
	return newTestRuntimeWithLimit(t, deployment, events, llm, 0)
}

func newTestRuntimeWithLimit(
	t *testing.T,
	deployment *Deployment,
	events *recordingAudit,
	llm model.LLM,
	maxRuns int,
) *Runtime {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "stub-engine", Version: "test"}, nil)
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
	runtime, err := NewRuntime(Config{
		Model:             "fake-model",
		Deployment:        deployment,
		AuditRecorder:     events,
		MaxConcurrentRuns: maxRuns,
		modelOverride:     llm,
		mcpTransportOverrides: map[string]mcp.Transport{
			"test": clientTransport,
		},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func plainTestDeployment(t *testing.T) *Deployment {
	t.Helper()
	deployment := NewTestDeployment(t)
	for index := range deployment.Spec.Agents {
		deployment.Spec.Agents[index].Output = OutputDefinition{}
		deployment.Spec.Agents[index].OutputSchema = nil
		deployment.Spec.Agents[index].ResolvedOutput = nil
		deployment.Spec.Agents[index].GenaiOutputSchema = nil
		deployment.Spec.Agents[index].SchemaDigest = ""
	}
	if err := deployment.validate(); err != nil {
		t.Fatalf("validate plain deployment: %v", err)
	}
	return deployment
}

// The deployment is the single source of truth for whether structured output
// is used. An explicit mode with a plain-text deployment carries no
// information and fails closed instead of being silently ignored.
func TestNewRuntimeRejectsRedundantStructuredOutputMode(t *testing.T) {
	_, err := NewRuntime(Config{
		Model:            "fake-model",
		Deployment:       plainTestDeployment(t),
		StructuredOutput: StructuredOutputJSONObject,
		AuditRecorder:    &recordingAudit{},
	})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStructuredOutputCapabilityInvalid {
		t.Fatalf("NewRuntime() error = %v, want %s", err, domain.CodeStructuredOutputCapabilityInvalid)
	}
}

func TestRuntimeRunReturnsPlainTextForGenericAgent(t *testing.T) {
	answer := "generic plain-text answer"
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: answer}}},
	}}})
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_plain", ThreadID: "thread_plain", UserID: "user_1", UserMessage: "hello",
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != answer {
		t.Fatalf("Text = %q, want %q", result.Text, answer)
	}
	if len(result.Output) != 0 {
		t.Fatalf("Output = %s, want empty", result.Output)
	}
	if got := len(events.eventsOfType(audit.EventRunCompleted)); got != 1 {
		t.Fatalf("completed run audit events = %d, want 1", got)
	}
}

func TestRuntimeRunRejectsDuplicateRunID(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "first"}}},
	}}})
	request := RunRequest{
		RunID: "run_duplicate", ThreadID: "thread_1", UserID: "user_1", UserMessage: "hello",
	}
	if _, err := runtime.Run(context.Background(), request, nil); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	_, err := runtime.Run(context.Background(), request, nil)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRunIDConflict {
		t.Fatalf("second Run() error = %v, want %s", err, domain.CodeRunIDConflict)
	}
}

func TestRuntimeRunRejectsWorkBeyondConcurrentRunLimit(t *testing.T) {
	events := &recordingAudit{}
	llm := &blockingLLM{started: make(chan struct{}), unblocked: make(chan struct{})}
	runtime := newTestRuntimeWithLimit(t, NewTestDeployment(t), events, llm, 1)
	firstDone := make(chan error, 1)
	go func() {
		_, err := runtime.Run(context.Background(), RunRequest{
			RunID: "run_active", ThreadID: "thread_1", UserID: "user_1", UserMessage: "first",
		}, nil)
		firstDone <- err
	}()

	select {
	case <-llm.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not reach the model")
	}
	limitedCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := runtime.Run(limitedCtx, RunRequest{
		RunID: "run_limited", ThreadID: "thread_1", UserID: "user_1", UserMessage: "second",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeRuntimeBusy {
		t.Fatalf("limited Run() error = %v, want %s", err, domain.CodeRuntimeBusy)
	}
	close(llm.unblocked)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
}

func TestRuntimeRunDeletesRunScopedSession(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "complete"}}},
	}}})
	request := RunRequest{
		RunID: "run_cleanup", ThreadID: "thread_1", UserID: "user_1", UserMessage: "hello",
	}
	if _, err := runtime.Run(context.Background(), request, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	response, err := runtime.sessions.Get(context.Background(), &session.GetRequest{
		AppName:   runtime.config.AppName,
		UserID:    request.UserID,
		SessionID: "run:" + request.RunID,
	})
	if err == nil && response != nil && response.Session != nil {
		t.Fatal("run-scoped ADK session survived terminal run")
	}
}

func TestRunAuditUsesEntrypointModelOverride(t *testing.T) {
	deployment := NewTestDeployment(t)
	deployment.Spec.Agents[0].Model = "artifact-model"
	events := &recordingAudit{}
	runtime := newTestRuntime(t, deployment, events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"ok"}`}}},
	}}})

	if _, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_model", ThreadID: "thread_model", UserID: "user_1", UserMessage: "hello",
	}, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	started := events.eventsOfType(audit.EventRunStarted)
	if len(started) != 1 || started[0].Model != "artifact-model" {
		t.Fatalf("run started model = %#v, want artifact-model", started)
	}
}

type failingDeleteSessionService struct {
	session.Service
}

func (s *failingDeleteSessionService) Delete(context.Context, *session.DeleteRequest) error {
	return errors.New("session backend unavailable")
}

func TestExternalSessionCleanupFailureDoesNotFlipSuccessfulRun(t *testing.T) {
	deployment := NewTestDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	events := &recordingAudit{}
	runtime, err := NewRuntime(Config{
		Model:             "fake-model",
		Deployment:        deployment,
		AuditRecorder:     events,
		MaxConcurrentRuns: 1,
		modelOverride: &fakeLLM{responses: []*model.LLMResponse{{
			Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"ok"}`}}},
		}}},
		SessionService: &failingDeleteSessionService{session.InMemoryService()},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	scope := AuditRunScope{
		RunID: "external_cleanup_failure", ThreadID: "external_context", UserID: "user_1", Protocol: "a2a",
	}
	if err := runtime.BeginAuditRun(context.Background(), "external_context", scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	err = runtime.EndAuditRun(context.Background(), "external_context", nil)
	if err != nil {
		t.Fatalf("EndAuditRun() error = %v, want successful execution despite cleanup failure", err)
	}
	completed := events.eventsOfType(audit.EventRunCompleted)
	failed := events.eventsOfType(audit.EventRunFailed)
	if len(completed) != 1 || len(failed) != 0 {
		t.Fatalf("run terminal events = completed:%d failed:%d, want 1/0", len(completed), len(failed))
	}
}

func TestExternalAuditRunDeletesExternalSession(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{})
	ctx := context.Background()
	scope := AuditRunScope{
		RunID:    "a2a_task",
		ThreadID: "a2a_context",
		UserID:   "user_1",
		Protocol: "a2a",
	}
	if err := runtime.BeginAuditRun(ctx, "a2a_context", scope); err != nil {
		t.Fatalf("BeginAuditRun() error = %v", err)
	}
	if err := runtime.EndAuditRun(ctx, "a2a_context", nil); err != nil {
		t.Fatalf("EndAuditRun() error = %v", err)
	}
	response, err := runtime.sessions.Get(ctx, &session.GetRequest{
		AppName:   runtime.config.AppName,
		UserID:    scope.UserID,
		SessionID: "a2a_context",
	})
	if err == nil && response != nil && response.Session != nil {
		t.Fatal("external ADK session survived terminal run")
	}
}

type terminalAuditRecorder struct {
	delegate *recordingAudit
}

func (r *terminalAuditRecorder) Record(_ context.Context, event *audit.Event) error {
	if event != nil && event.Type == audit.EventRunCompleted {
		return domain.NewError(audit.CodeAuditAppendFailed, "terminal audit write failed", nil)
	}
	return r.delegate.Record(context.Background(), event)
}

func TestRuntimeRunFailsClosedWhenTerminalAuditWriteFails(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "complete"}}},
	}}})
	failing := &terminalAuditRecorder{delegate: events}
	runtime.config.AuditRecorder = failing
	_, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_audit_fail", ThreadID: "thread_1", UserID: "user_1", UserMessage: "hello",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != audit.CodeAuditAppendFailed {
		t.Fatalf("Run() error = %v, want terminal audit failure", err)
	}
}

// TestRuntimeRunExecutesMCPToolBeforeAuditCompletion prevents an observability
// callback from accidentally replacing the tool invocation with its arguments.
func TestRuntimeRunExecutesMCPToolBeforeAuditCompletion(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &turnBasedLLM{turns: []*model.LLMResponse{
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "call_tool", Name: "test_tool"}},
		}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "final answer"}}}},
	}})

	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_tool", ThreadID: "thread_tool", UserID: "user_1", UserMessage: "use tool",
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "final answer" {
		t.Fatalf("Text = %q, want final answer", result.Text)
	}
	completed := events.eventsOfType(audit.EventToolCallCompleted)
	if len(completed) != 1 {
		t.Fatalf("completed tool audit events = %d, want 1", len(completed))
	}
	inputDigest, _ := completed[0].Payload["input_digest"].(string)
	outputDigest, _ := completed[0].Payload["output_digest"].(string)
	if inputDigest == "" || outputDigest == "" {
		t.Fatalf("tool audit digests = %#v", completed[0].Payload)
	}
	if inputDigest == outputDigest {
		t.Fatal("tool output digest equals input digest; ADK did not execute the tool")
	}
}

// TestRuntimeRunProducesStructuredAnswerWithoutPayloadLeak is the keystone
// regression test for ADK structured output: the Runner clears
// Event.Output before yielding, so the runtime must read the parsed answer
// from session state, must never expose payload JSON to protocol observers,
// and must still persist the LLM audit lifecycle.
func TestRuntimeRunProducesStructuredAnswerWithoutPayloadLeak(t *testing.T) {
	payload := `{"answer":"八字显示压力而非确定性。","confidence":0.82}`
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: payload}}},
	}}})

	observed := make([]*session.Event, 0)
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID:       "run_1",
		ThreadID:    "thread_1",
		UserID:      "user_1",
		UserMessage: "看事业",
	}, func(event *session.Event) error {
		observed = append(observed, event)
		return nil
	})
	if err != nil {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("caused by: %v", cause)
		}
		t.Fatalf("Run() error = %v", err)
	}

	if result.Text != "八字显示压力而非确定性。" {
		t.Fatalf("Text = %q, want the parsed answer", result.Text)
	}
	if result.Definition.Name != "test-agent" || result.Definition.Version != "1.0.0" {
		t.Fatalf("definition ref = %+v", result.Definition)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	for index, event := range observed {
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.Text != "" && !part.Thought {
				t.Fatalf("observed[%d] leaks model payload text %q", index, part.Text)
			}
		}
	}
	for _, eventType := range []audit.EventType{
		audit.EventRunStarted,
		audit.EventLLMCallStarted,
		audit.EventLLMCallCompleted,
		audit.EventRunCompleted,
	} {
		if got := len(events.eventsOfType(eventType)); got != 1 {
			t.Fatalf("%s audit events = %d, want 1", eventType, got)
		}
	}
	if strings.Contains(result.Text, `\"answer\"`) {
		t.Fatal("answer still contains JSON payload markers")
	}
}

// TestStructuredAnswerConsistentAcrossProtocolSources proves the two protocol
// adapters derive the same user-facing answer from one run: AG-UI reads the
// validated session-state value (RunResult.Text) while A2A reads the event
// StateDelta, and both must finalize through ParseStructuredOutput.
func TestStructuredAnswerConsistentAcrossProtocolSources(t *testing.T) {
	deployment := NewTestDeployment(t)
	entrypoint, err := deployment.EntrypointDefinition()
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"answer":"一致结论","confidence":0.88}`
	events := &recordingAudit{}
	runtime := newTestRuntime(t, deployment, events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: payload}}},
	}}})

	var stateDelta any
	var sawDelta bool
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_consistency", ThreadID: "thread_consistency", UserID: "user_1", UserMessage: "看事业",
	}, func(event *session.Event) error {
		if event != nil && event.Actions.StateDelta != nil {
			if value, ok := event.Actions.StateDelta[StructuredOutputStateKey(entrypoint.Name)]; ok {
				stateDelta = value
				sawDelta = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !sawDelta {
		t.Fatal("structured entrypoint run did not project an observable StateDelta")
	}
	a2aOutput, err := ParseStructuredOutput(stateDelta, entrypoint)
	if err != nil {
		t.Fatalf("ParseStructuredOutput(StateDelta) error = %v", err)
	}
	if result.Text != a2aOutput.Text {
		t.Fatalf("AG-UI answer %q != A2A answer %q", result.Text, a2aOutput.Text)
	}
	if result.Text != "一致结论" {
		t.Fatalf("answer = %q, want the pointer-selected text", result.Text)
	}
}

func TestRuntimeRunEmptyResponse(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: ""}}},
	}}})
	_, runErr := runtime.Run(context.Background(), RunRequest{
		RunID: "run_empty", ThreadID: "thread_1", UserID: "user_1", UserMessage: "看事业",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(runErr, &domainErr) || domainErr.Code != domain.CodeRuntimeEmptyResponse {
		t.Fatalf("Run() error = %v, want %s", runErr, domain.CodeRuntimeEmptyResponse)
	}
	if got := len(events.eventsOfType(audit.EventRunFailed)); got != 1 {
		t.Fatalf("failed run audit events = %d, want 1", got)
	}
}

func TestRuntimeRunMarksObserverFailureAsFailedRun(t *testing.T) {
	observerErr := errors.New("protocol write failed")
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{Text: `{"answer":"ok"}`},
			{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "test_tool"}},
		}},
	}}})
	_, runErr := runtime.Run(context.Background(), RunRequest{
		RunID: "run_observer", ThreadID: "thread_1", UserID: "user_1", UserMessage: "test",
	}, func(*session.Event) error {
		return observerErr
	})
	if !errors.Is(runErr, observerErr) {
		t.Fatalf("Run() error = %v, want observer error", runErr)
	}
	if got := len(events.eventsOfType(audit.EventRunFailed)); got != 1 {
		t.Fatalf("failed run audit events = %d, want 1", got)
	}
}

func TestRuntimeRunContextCancelled(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "答案"}}},
	}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, runErr := runtime.Run(ctx, RunRequest{
		RunID: "run_cancel", ThreadID: "thread_1", UserID: "user_1", UserMessage: "看事业",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(runErr, &domainErr) || domainErr.Code != domain.CodeRuntimeCancelled {
		t.Fatalf("Run() error = %v, want %s", runErr, domain.CodeRuntimeCancelled)
	}
}

func TestRuntimeRunLLMError(t *testing.T) {
	events := &recordingAudit{}
	llmErr := errors.New("model overloaded")
	runtime := newTestRuntime(t, NewTestDeployment(t), events, &fakeLLM{err: llmErr})
	_, runErr := runtime.Run(context.Background(), RunRequest{
		RunID: "run_err", ThreadID: "thread_1", UserID: "user_1", UserMessage: "看事业",
	}, nil)
	var domainErr *domain.Error
	if !errors.As(runErr, &domainErr) {
		t.Fatalf("Run() error = %v, want domain error", runErr)
	}
	if domainErr.Code != domain.CodeRuntimeFailed {
		t.Fatalf("error code = %s, want %s", domainErr.Code, domain.CodeRuntimeFailed)
	}
	if got := len(events.eventsOfType(audit.EventLLMCallFailed)); got != 1 {
		t.Fatalf("failed LLM audit events = %d, want 1", got)
	}
}

// NewTestDeployment exposes the generic test deployment to external adapter
// tests compiled into the same test binary.
func NewTestDeployment(t testing.TB) *Deployment {
	t.Setenv("TEST_MCP_ENDPOINT", "in-memory://test")
	deployment, err := testDeployment(t)
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func TestRuntimeBuildsStandardADKAgentGraph(t *testing.T) {
	deployment := NewTestDeployment(t)
	// Convert the generic single-agent test deployment into a two-Agent tree.
	deployment.Spec.Agents[0].Name = "coordinator"
	deployment.Spec.Agents[0].SubAgents = []AgentReference{{Name: "worker"}}
	deployment.Spec.Agents = append(deployment.Spec.Agents, AgentDefinition{
		Name:        "worker",
		Version:     "1.0.0",
		Description: "generic worker agent",
		Mode:        AgentModeTask,
		Instruction: deployment.Spec.Agents[0].Instruction,
		Output:      deployment.Spec.Agents[0].Output,
		Tools:       ToolAllowlist{Allow: map[string][]string{"test": {"test_tool"}}},
	})
	if err := deployment.validate(); err != nil {
		t.Fatalf("validate multi-agent deployment: %v", err)
	}
	for index := range deployment.Spec.Agents {
		agent := &deployment.Spec.Agents[index]
		if err := agent.validate(map[string]struct{}{"test": {}}); err != nil {
			t.Fatalf("validate agent %q: %v", agent.Name, err)
		}
	}

	events := &recordingAudit{}
	runtime := newTestRuntime(t, deployment, events, &fakeLLM{responses: []*model.LLMResponse{{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: `{"answer":"ok"}`}}},
	}}})
	if runtime.entrypoint.Name != "coordinator" {
		t.Fatalf("entrypoint = %q", runtime.entrypoint.Name)
	}
	worker := runtime.RootAgent().FindSubAgent("worker")
	if worker == nil {
		t.Fatal("worker sub-agent was not registered with ADK")
	}
	if worker.Name() != "worker" {
		t.Fatalf("worker name = %q", worker.Name())
	}
}

// TestRuntimeRunTerminalEvidenceAfterChildLifecycles guards the terminal write
// order on the AG-UI (Run) path: child lifecycle failures must be appended
// before the run's terminal event, matching EndAuditRun's ordering.
func TestRuntimeRunTerminalEvidenceAfterChildLifecycles(t *testing.T) {
	events := &recordingAudit{}
	runtime := newTestRuntime(t, plainTestDeployment(t), events, &fakeLLM{err: errors.New("model unavailable")})
	_, runErr := runtime.Run(context.Background(), RunRequest{
		RunID: "run_order", ThreadID: "thread_order", UserID: "user_1", UserMessage: "x",
	}, nil)
	if runErr == nil {
		t.Fatal("expected run failure")
	}
	indexOf := func(want audit.EventType) int {
		for index, event := range events.events {
			if event.Type == want {
				return index
			}
		}
		return -1
	}
	llmFailed := indexOf(audit.EventLLMCallFailed)
	runFailed := indexOf(audit.EventRunFailed)
	if llmFailed < 0 || runFailed < 0 {
		t.Fatalf("missing terminal evidence: llm.failed=%d run.failed=%d", llmFailed, runFailed)
	}
	if runFailed <= llmFailed {
		t.Fatalf("run.failed (idx %d) must follow llm.call.failed (idx %d)", runFailed, llmFailed)
	}
}
