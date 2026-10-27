package a2a_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	a2atypes "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	a2apush "github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/ml8s/agent-runtime/internal/agent"
	"github.com/ml8s/agent-runtime/internal/audit"
	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/execution"
	"github.com/ml8s/agent-runtime/internal/protocol/a2a"
	protocolstate "github.com/ml8s/agent-runtime/internal/protocolstate/sqlite"
	"github.com/ml8s/agent-runtime/internal/testagent"
)

type testAuditRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *testAuditRecorder) Record(_ context.Context, event *audit.Event) error {
	if event == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, *event)
	return nil
}

func (r *testAuditRecorder) eventsOfType(eventType audit.EventType) []audit.Event {
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

func testRuntime(t *testing.T) (*agent.Runtime, *testAuditRecorder) {
	return testRuntimeWithDeployment(t, testagent.AgentDeployment(t))
}

func testRuntimeWithDeployment(t *testing.T, deployment *deploymentpkg.AgentDeployment) (*agent.Runtime, *testAuditRecorder) {
	t.Helper()
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected model path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id":"resp_a2a",
			"model":"test-model",
			"status":"completed",
			"output":[{"type":"message","content":[{"type":"output_text","text":"{\"answer\":\"hello\"}"}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)
	}))
	t.Cleanup(modelServer.Close)
	recorder := &testAuditRecorder{}
	runtime, err := agent.NewRuntime(agent.Config{
		Model:            "test-model",
		ModelAPIKey:      "test-key",
		ModelBaseURL:     modelServer.URL + "/v1",
		AgentDeployment:  deployment,
		StructuredOutput: execution.StructuredOutputJSONSchema,
		Provider:         "test-provider",
		AuditRecorder:    recorder,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime, recorder
}

func TestAgentCardDeclaresStandardJSONRPCBinding(t *testing.T) {
	publicURL, err := url.Parse("https://agent.internal")
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := newA2AServer(t, publicURL, func(config *a2a.Config) {
		config.Security.Bearer = true
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Agent Card status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Agent Card content type = %q", got)
	}
	var card a2atypes.AgentCard
	if err := json.Unmarshal(response.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode Agent Card: %v", err)
	}
	if card.Name != "main" || card.Version != "1.0.0" {
		t.Fatalf("card identity = %q/%q", card.Name, card.Version)
	}
	if len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].URL != "https://agent.internal/a2a" {
		t.Fatalf("supported interfaces = %+v", card.SupportedInterfaces)
	}
	if !card.Capabilities.Streaming {
		t.Fatal("A2A streaming capability was not declared")
	}
	if card.Capabilities.PushNotifications {
		t.Fatal("unconfigured A2A push capability was declared")
	}
	if !contains(card.DefaultOutputModes, "text/plain") || !contains(card.DefaultOutputModes, "application/json") {
		t.Fatalf("A2A output modes = %v, want text and structured JSON", card.DefaultOutputModes)
	}
	if _, ok := card.SecuritySchemes["agent_runtime_service_bearer"]; !ok {
		t.Fatal("A2A bearer security scheme was not declared")
	}
	if len(card.SecurityRequirements) != 1 {
		t.Fatalf("security requirements = %+v", card.SecurityRequirements)
	}
	if len(card.Skills) != 1 || card.Skills[0].ID != "main" {
		t.Fatalf("skills = %+v, want one entrypoint skill", card.Skills)
	}
	if !contains(card.Skills[0].Tags, "entrypoint") {
		t.Fatalf("entrypoint skill tags = %+v", card.Skills[0].Tags)
	}
	if strings.Contains(response.Body.String(), "You are a generic adapter test agent.") {
		t.Fatal("Agent Card leaked Agent instruction")
	}
	if len(card.Capabilities.Extensions) != 0 {
		t.Fatalf("Agent Card extensions = %+v; private protocol extensions must not be advertised", card.Capabilities.Extensions)
	}
}

func TestAgentCardHidesInternalSubAgentsAndTools(t *testing.T) {
	deployment := testagent.AgentDeployment(t)
	deployment.Spec.Agents[0].Name = "coordinator"
	deployment.Spec.Agents[0].SubAgents = []deploymentpkg.AgentReference{{Name: "worker"}}
	worker := deployment.Spec.Agents[0]
	worker.Name = "worker"
	worker.Description = "generic worker"
	worker.Mode = deploymentpkg.AgentModeTask
	worker.SubAgents = nil
	worker.Tools = deploymentpkg.ToolAllowlist{Allow: map[string][]string{
		"test":         {"test_tool"},
		"skilltoolset": {"list_skills", "load_skill", "load_skill_resource"},
	}}
	deployment.Spec.Agents = append(deployment.Spec.Agents, worker)
	if err := deployment.Validate(); err != nil {
		t.Fatalf("validate deployment: %v", err)
	}

	publicURL, err := url.Parse("https://agent.internal")
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := newA2AServerWithDeployment(t, publicURL, deployment)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Agent Card status = %d, body = %s", response.Code, response.Body.String())
	}
	var card a2atypes.AgentCard
	if err := json.Unmarshal(response.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode Agent Card: %v", err)
	}
	if len(card.Skills) != 1 || card.Skills[0].ID != "coordinator" {
		t.Fatalf("skills = %+v, want only entrypoint", card.Skills)
	}
	for _, tag := range card.Skills[0].Tags {
		if strings.HasPrefix(tag, "tool:") || strings.HasPrefix(tag, "mode:") || strings.HasPrefix(tag, "adk:") {
			t.Fatalf("public skill leaked internal topology tag %q", tag)
		}
	}
	if strings.Contains(response.Body.String(), "generic worker") {
		t.Fatal("Agent Card leaked internal sub-agent")
	}
	if strings.Contains(response.Body.String(), "You are a generic adapter test agent.") {
		t.Fatal("Agent Card leaked Agent instruction")
	}
}

func newA2AServer(t *testing.T, publicURL *url.URL, overrides ...func(*a2a.Config)) (*a2a.Server, *testAuditRecorder, error) {
	t.Helper()
	runtime, recorder := testRuntime(t)
	config := a2a.Config{PublicURL: publicURL}
	for _, override := range overrides {
		override(&config)
	}
	server, err := a2a.New(runtime, config)
	return server, recorder, err
}

func newA2AServerWithDeployment(
	t *testing.T,
	publicURL *url.URL,
	deployment *deploymentpkg.AgentDeployment,
) (*a2a.Server, *testAuditRecorder, error) {
	t.Helper()
	runtime, recorder := testRuntimeWithDeployment(t, deployment)
	server, err := a2a.New(runtime, a2a.Config{PublicURL: publicURL})
	return server, recorder, err
}

func TestA2AJSONRPCExecutesRuntimeAndClosesAudit(t *testing.T) {
	publicURL, err := url.Parse("https://agent.internal")
	if err != nil {
		t.Fatal(err)
	}
	deployment := testagent.AgentDeployment(t)
	deployment.Spec.Agents[0].Tools.Allow = map[string][]string{
		"skilltoolset": {"list_skills", "load_skill", "load_skill_resource"},
	}
	if err := deployment.Validate(); err != nil {
		t.Fatalf("validate deployment: %v", err)
	}
	server, recorder, err := newA2AServerWithDeployment(t, publicURL, deployment)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	endpoint := httptest.NewServer(server.EndpointHandler())
	t.Cleanup(endpoint.Close)

	cardResponse := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(cardResponse, httptest.NewRequest(http.MethodGet, "/card", nil))
	var card a2atypes.AgentCard
	if err := json.Unmarshal(cardResponse.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode Agent Card: %v", err)
	}
	card.SupportedInterfaces[0].URL = endpoint.URL

	ctx := context.Background()
	client, err := a2aclient.NewFromCard(ctx, &card, a2aclient.WithJSONRPCTransport(nil))
	if err != nil {
		t.Fatalf("NewFromCard() error = %v", err)
	}
	message := a2atypes.NewMessage(a2atypes.MessageRoleUser, a2atypes.NewTextPart("hello"))
	message.ContextID = "same_context"
	result, err := client.SendMessage(ctx, &a2atypes.SendMessageRequest{Message: message})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if result == nil {
		t.Fatal("SendMessage() returned no result")
	}
	task, taskOK := result.(*a2atypes.Task)
	if !taskOK || len(task.Artifacts) == 0 || len(task.Artifacts[0].Parts) == 0 {
		raw, _ := json.Marshal(result)
		t.Fatalf("SendMessage result = %#v; json=%s", result, raw)
	}
	data, dataOK := task.Artifacts[0].Parts[0].Content.(a2atypes.Data)
	if !dataOK || data.Value == nil {
		t.Fatalf("A2A structured artifact = %#v, want data part", task.Artifacts[0].Parts[0].Content)
	}
	started := recorder.eventsOfType(audit.EventRunStarted)
	completed := recorder.eventsOfType(audit.EventRunCompleted)
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("run audit events = %d/%d, want 1/1", len(started), len(completed))
	}
	if started[0].Protocol != "a2a" || completed[0].Protocol != "a2a" {
		t.Fatalf("A2A audit protocol = %q/%q", started[0].Protocol, completed[0].Protocol)
	}
	if completed[0].ID == started[0].ID {
		t.Fatal("A2A terminal audit ID collided with started ID")
	}
	if completed[0].RunID == completed[0].ParentRunID || completed[0].ParentRunID == "" {
		t.Fatalf("A2A attempt/task IDs = %q/%q, want distinct attempt and task", completed[0].RunID, completed[0].ParentRunID)
	}

	// A standard ContextID can carry multiple sequential execution attempts.
	// Attempt state is process-local and attempt-scoped; only the ADK session
	// is ContextID-scoped.
	for range 2 {
		message := a2atypes.NewMessage(a2atypes.MessageRoleUser, a2atypes.NewTextPart("again"))
		message.ContextID = "same_context"
		if _, err := client.SendMessage(ctx, &a2atypes.SendMessageRequest{Message: message}); err != nil {
			t.Fatalf("SendMessage(same context) error = %v", err)
		}
	}
	started = recorder.eventsOfType(audit.EventRunStarted)
	completed = recorder.eventsOfType(audit.EventRunCompleted)
	if len(started) != 3 || len(completed) != 3 {
		t.Fatalf("same-context audit events = %d/%d, want 3/3", len(started), len(completed))
	}
	for index := range started {
		if started[index].ThreadID != "same_context" || completed[index].ThreadID != "same_context" {
			t.Fatalf("audit attempt %d lost ContextID correlation", index)
		}
	}
	if started[0].RunID == started[1].RunID ||
		started[1].RunID == started[2].RunID ||
		started[0].RunID == started[2].RunID {
		t.Fatalf("attempts did not receive unique IDs: %q/%q/%q", started[0].RunID, started[1].RunID, started[2].RunID)
	}
}

func TestA2AUsesInjectedOfficialTaskStore(t *testing.T) {
	publicURL, err := url.Parse("https://agent.internal")
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := testRuntime(t)
	store := taskstore.NewInMemory(nil)
	server, err := a2a.New(runtime, a2a.Config{
		PublicURL: publicURL,
		TaskStore: store,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	endpoint := httptest.NewServer(server.EndpointHandler())
	t.Cleanup(endpoint.Close)

	cardResponse := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(cardResponse, httptest.NewRequest(http.MethodGet, "/card", nil))
	var card a2atypes.AgentCard
	if err := json.Unmarshal(cardResponse.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode Agent Card: %v", err)
	}
	card.SupportedInterfaces[0].URL = endpoint.URL
	client, err := a2aclient.NewFromCard(context.Background(), &card, a2aclient.WithJSONRPCTransport(nil))
	if err != nil {
		t.Fatalf("NewFromCard() error = %v", err)
	}
	result, err := client.SendMessage(context.Background(), &a2atypes.SendMessageRequest{
		Message: a2atypes.NewMessage(a2atypes.MessageRoleUser, a2atypes.NewTextPart("hello")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2atypes.Task)
	if !ok {
		t.Fatalf("SendMessage result = %#v, want task", result)
	}
	stored, err := store.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("injected TaskStore Get(%s) error = %v", task.ID, err)
	}
	if stored.Task == nil || stored.Task.ID != task.ID {
		t.Fatalf("stored task = %#v, want task %s", stored.Task, task.ID)
	}
}

func TestA2ADurableTaskStoreSupportsStandardGetAndList(t *testing.T) {
	publicURL, err := url.Parse("https://agent.internal")
	if err != nil {
		t.Fatal(err)
	}
	stateDB, err := protocolstate.Open(filepath.Join(t.TempDir(), "protocol.db"))
	if err != nil {
		t.Fatalf("open protocol state = %v", err)
	}
	t.Cleanup(func() { _ = stateDB.Close() })
	runtime, _ := testRuntime(t)
	server, err := a2a.New(runtime, a2a.Config{
		PublicURL: publicURL,
		TaskStore: stateDB.NewTaskStore(protocolstate.TaskStoreConfig{}),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	endpoint := httptest.NewServer(server.EndpointHandler())
	t.Cleanup(endpoint.Close)

	cardResponse := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(cardResponse, httptest.NewRequest(http.MethodGet, "/card", nil))
	var card a2atypes.AgentCard
	if err := json.Unmarshal(cardResponse.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode Agent Card: %v", err)
	}
	card.SupportedInterfaces[0].URL = endpoint.URL
	client, err := a2aclient.NewFromCard(context.Background(), &card, a2aclient.WithJSONRPCTransport(nil))
	if err != nil {
		t.Fatalf("NewFromCard() error = %v", err)
	}
	result, err := client.SendMessage(context.Background(), &a2atypes.SendMessageRequest{
		Message: a2atypes.NewMessage(a2atypes.MessageRoleUser, a2atypes.NewTextPart("hello")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	created, ok := result.(*a2atypes.Task)
	if !ok {
		t.Fatalf("SendMessage result = %#v, want task", result)
	}
	got, err := client.GetTask(context.Background(), &a2atypes.GetTaskRequest{ID: created.ID})
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got.ID != created.ID || got.ContextID != created.ContextID {
		t.Fatalf("GetTask() = %#v, want created task", got)
	}
	listed, err := client.ListTasks(context.Background(), &a2atypes.ListTasksRequest{PageSize: 100})
	if err != nil {
		t.Fatalf("ListTasks() error = %v", err)
	}
	if listed.TotalSize != 1 || len(listed.Tasks) != 1 || listed.Tasks[0].ID != created.ID {
		t.Fatalf("ListTasks() = %#v, want created task", listed)
	}
}

type recordingA2AMetrics struct {
	mu       sync.Mutex
	configs  map[string]int
	delivery map[string]int
}

func newRecordingA2AMetrics() *recordingA2AMetrics {
	return &recordingA2AMetrics{configs: map[string]int{}, delivery: map[string]int{}}
}

func (m *recordingA2AMetrics) ObservePushConfig(operation, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[operation+"/"+status]++
}

func (m *recordingA2AMetrics) ObservePushDelivery(status string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivery[status]++
}

func (m *recordingA2AMetrics) totals() (map[string]int, map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	configs := make(map[string]int, len(m.configs))
	for key, value := range m.configs {
		configs[key] = value
	}
	delivery := make(map[string]int, len(m.delivery))
	for key, value := range m.delivery {
		delivery[key] = value
	}
	return configs, delivery
}

type recordingPushSender struct {
	mu     sync.Mutex
	events []a2atypes.Event
}

func (s *recordingPushSender) SendPush(_ context.Context, _ *a2atypes.PushConfig, event a2atypes.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingPushSender) snapshot() []a2atypes.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]a2atypes.Event(nil), s.events...)
}

func TestA2APushNotificationsUseOfficialSDK(t *testing.T) {
	publicURL, err := url.Parse("https://agent.internal")
	if err != nil {
		t.Fatal(err)
	}
	pushStore := a2apush.NewInMemoryStore()
	sender := &recordingPushSender{}
	pushMetrics := newRecordingA2AMetrics()
	runtime, _ := testRuntime(t)
	server, err := a2a.New(runtime, a2a.Config{
		PublicURL: publicURL,
		Metrics:   pushMetrics,
		Push: a2a.PushConfig{
			Store:  pushStore,
			Sender: sender,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	endpoint := httptest.NewServer(server.EndpointHandler())
	t.Cleanup(endpoint.Close)

	cardResponse := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(cardResponse, httptest.NewRequest(http.MethodGet, "/card", nil))
	var card a2atypes.AgentCard
	if err := json.Unmarshal(cardResponse.Body.Bytes(), &card); err != nil {
		t.Fatalf("decode Agent Card: %v", err)
	}
	if !card.Capabilities.PushNotifications {
		t.Fatal("configured A2A push capability was not declared")
	}
	card.SupportedInterfaces[0].URL = endpoint.URL
	client, err := a2aclient.NewFromCard(context.Background(), &card, a2aclient.WithJSONRPCTransport(nil))
	if err != nil {
		t.Fatalf("NewFromCard() error = %v", err)
	}
	result, err := client.SendMessage(context.Background(), &a2atypes.SendMessageRequest{
		Config: &a2atypes.SendMessageConfig{
			PushConfig: &a2atypes.PushConfig{
				URL:   "https://notifications.example/inline",
				Token: "inline-token",
			},
		},
		Message: a2atypes.NewMessage(a2atypes.MessageRoleUser, a2atypes.NewTextPart("hello")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	task, ok := result.(*a2atypes.Task)
	if !ok {
		t.Fatalf("SendMessage result = %#v, want task", result)
	}
	inlineConfigs, err := client.ListTaskPushConfigs(context.Background(), &a2atypes.ListTaskPushConfigRequest{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("ListTaskPushConfigs() for inline config error = %v", err)
	}
	if len(inlineConfigs) != 1 {
		t.Fatalf("inline push configs = %#v, want one", inlineConfigs)
	}

	saved, err := client.CreateTaskPushConfig(context.Background(), &a2atypes.PushConfig{
		TaskID: task.ID,
		URL:    "https://notifications.example/a2a",
		Token:  "receiver-token",
	})
	if err != nil {
		t.Fatalf("CreateTaskPushConfig() error = %v", err)
	}
	if saved == nil || saved.ID == "" || saved.TaskID != task.ID {
		t.Fatalf("saved push config = %#v, want generated ID and task ID", saved)
	}
	got, err := client.GetTaskPushConfig(context.Background(), &a2atypes.GetTaskPushConfigRequest{
		TaskID: task.ID,
		ID:     saved.ID,
	})
	if err != nil {
		t.Fatalf("GetTaskPushConfig() error = %v", err)
	}
	if got == nil || got.ID != saved.ID || got.URL != saved.URL || got.Token != saved.Token {
		t.Fatalf("GetTaskPushConfig() = %#v, want saved config", got)
	}
	configs, err := client.ListTaskPushConfigs(context.Background(), &a2atypes.ListTaskPushConfigRequest{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("ListTaskPushConfigs() error = %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("push configs = %#v, want inline and created configs", configs)
	}
	if err := client.DeleteTaskPushConfig(context.Background(), &a2atypes.DeleteTaskPushConfigRequest{
		TaskID: task.ID,
		ID:     saved.ID,
	}); err != nil {
		t.Fatalf("DeleteTaskPushConfig() error = %v", err)
	}
	configs, err = client.ListTaskPushConfigs(context.Background(), &a2atypes.ListTaskPushConfigRequest{
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatalf("ListTaskPushConfigs() after delete error = %v", err)
	}
	if len(configs) != 1 || configs[0].ID != inlineConfigs[0].ID {
		t.Fatalf("push configs after delete = %#v, want inline config", configs)
	}
	if _, err := client.CreateTaskPushConfig(context.Background(), &a2atypes.PushConfig{
		TaskID: task.ID,
		URL:    "http://notifications.example/a2a",
	}); err == nil {
		t.Fatal("CreateTaskPushConfig() accepted an HTTP callback URL")
	}
	if _, err := client.CreateTaskPushConfig(context.Background(), &a2atypes.PushConfig{
		TaskID: task.ID,
		URL:    "https://notifications.example/a2a",
		Auth:   &a2atypes.PushAuthInfo{Scheme: "hmac", Credentials: "secret"},
	}); err == nil {
		t.Fatal("CreateTaskPushConfig() accepted an unsupported callback auth scheme")
	}

	configMetrics, deliveryMetrics := pushMetrics.totals()
	if configMetrics["save/success"] == 0 {
		t.Fatalf("push config metrics = %#v, want successful saves", configMetrics)
	}
	if deliveryMetrics["success"] == 0 {
		t.Fatalf("push delivery metrics = %#v, want successful delivery", deliveryMetrics)
	}

	events := sender.snapshot()
	if len(events) == 0 {
		t.Fatal("A2A SDK sent no push notifications")
	}
	var sawStatusUpdate bool
	for _, event := range events {
		if _, ok := event.(*a2atypes.TaskStatusUpdateEvent); ok {
			sawStatusUpdate = true
		}
	}
	if !sawStatusUpdate {
		t.Fatalf("push events = %#v, want task status update", events)
	}
}

func TestA2APartialPushConfigurationFailsClosed(t *testing.T) {
	publicURL, err := url.Parse("https://agent.internal")
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := testRuntime(t)
	_, err = a2a.New(runtime, a2a.Config{
		PublicURL: publicURL,
		Push:      a2a.PushConfig{Store: a2apush.NewInMemoryStore()},
	})
	if err == nil || !strings.Contains(err.Error(), "both config store and sender") {
		t.Fatalf("New() error = %v, want partial push dependency failure", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
