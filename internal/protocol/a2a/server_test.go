package a2a_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	a2atypes "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/ml8s/liki-agent-runtime/internal/agent"
	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/protocol/a2a"
	"github.com/ml8s/liki-agent-runtime/internal/testagent"
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
	return testRuntimeWithDeployment(t, testagent.Deployment(t))
}

func testRuntimeWithDeployment(t *testing.T, deployment *agent.Deployment) (*agent.Runtime, *testAuditRecorder) {
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
		Deployment:       deployment,
		StructuredOutput: agent.StructuredOutputJSONSchema,
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
	server, _, err := newA2AServer(t, publicURL)
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
	if !contains(card.DefaultOutputModes, "text/plain") || !contains(card.DefaultOutputModes, "application/json") {
		t.Fatalf("A2A output modes = %v, want text and structured JSON", card.DefaultOutputModes)
	}
	if _, ok := card.SecuritySchemes["liki_service_bearer"]; !ok {
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

func TestAgentCardDescribesSubAgentsWithoutInstructions(t *testing.T) {
	deployment := testagent.Deployment(t)
	deployment.Spec.Agents[0].Name = "coordinator"
	deployment.Spec.Agents[0].SubAgents = []agent.AgentReference{{Name: "worker"}}
	worker := deployment.Spec.Agents[0]
	worker.Name = "worker"
	worker.Description = "generic worker"
	worker.Mode = agent.AgentModeTask
	worker.SubAgents = nil
	worker.Tools = agent.ToolAllowlist{Allow: map[string][]string{
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
	if len(card.Skills) != 2 {
		t.Fatalf("skills = %+v, want entrypoint and sub-agent", card.Skills)
	}
	bySkillID := map[string]a2atypes.AgentSkill{}
	for _, skill := range card.Skills {
		bySkillID[skill.ID] = skill
	}
	coordinator, ok := bySkillID["coordinator"]
	if !ok || !contains(coordinator.Tags, "entrypoint") {
		t.Fatalf("coordinator skill = %+v", coordinator)
	}
	workerSkill, ok := bySkillID["worker"]
	if !ok || !contains(workerSkill.Tags, "tool:test/test_tool") {
		t.Fatalf("worker skill = %+v", workerSkill)
	}
	if strings.Contains(response.Body.String(), "You are a generic adapter test agent.") {
		t.Fatal("Agent Card leaked Agent instruction")
	}
}

func newA2AServer(t *testing.T, publicURL *url.URL) (*a2a.Server, *testAuditRecorder, error) {
	t.Helper()
	runtime, recorder := testRuntime(t)
	server, err := a2a.New(runtime, a2a.Config{PublicURL: publicURL})
	return server, recorder, err
}

func newA2AServerWithDeployment(
	t *testing.T,
	publicURL *url.URL,
	deployment *agent.Deployment,
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
	deployment := testagent.Deployment(t)
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
	result, err := client.SendMessage(ctx, &a2atypes.SendMessageRequest{
		Message: a2atypes.NewMessage(a2atypes.MessageRoleUser, a2atypes.NewTextPart("hello")),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if result == nil {
		t.Fatal("SendMessage() returned no result")
	}
	task, taskOK := result.(*a2atypes.Task)
	if !taskOK || len(task.Artifacts) == 0 || len(task.Artifacts[0].Parts) == 0 {
		t.Fatalf("SendMessage result = %#v, want task artifact", result)
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

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
