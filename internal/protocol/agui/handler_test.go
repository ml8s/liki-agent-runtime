package agui_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/agent"
	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/notify"
	"github.com/ml8s/liki-agent-runtime/internal/platform/identity"
	"github.com/ml8s/liki-agent-runtime/internal/protocol/agui"
	"github.com/ml8s/liki-agent-runtime/internal/testagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type recordingAuditEvents struct{}

func (r *recordingAuditEvents) Record(_ context.Context, _ *audit.Event) error {
	return nil
}

func newRuntime(t *testing.T) *agent.Runtime {
	t.Helper()
	deployment := testagent.AgentDeployment(t)
	t.Setenv("TEST_MCP_ENDPOINT", "http://127.0.0.1:9/mcp")
	runtime, err := agent.NewRuntime(agent.Config{
		Model:            "test-model",
		ModelAPIKey:      "test-key",
		AgentDeployment:  deployment,
		StructuredOutput: agent.StructuredOutputJSONSchema,
		AuditRecorder:    &recordingAuditEvents{},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func TestAGUIRequiresPost(t *testing.T) {
	handler, err := agui.New(newRuntime(t), agui.Config{RunTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ag-ui", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("content type = %q, want problem+json", response.Header().Get("Content-Type"))
	}
	if !strings.Contains(response.Body.String(), `"code":"method_not_allowed"`) {
		t.Fatalf("body lacks stable error code: %s", response.Body.String())
	}
}

func TestAGUIRejectsMalformedInput(t *testing.T) {
	handler, err := agui.New(newRuntime(t), agui.Config{RunTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader("{"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAGUIRejectsOversizedIdentifiersBeforeStreamStarts(t *testing.T) {
	handler, err := agui.New(&capturingRuntime{capture: &agent.RunRequest{}}, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"threadId":%q,"messages":[{"role":"user","content":"hi"}]}`, strings.Repeat("t", 129))
	request := authenticatedRequest(t, body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized identifier status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	if strings.Contains(response.Body.String(), "RUN_STARTED") {
		t.Fatalf("stream started before validation: %s", response.Body.String())
	}
}

type scriptedRuntime struct {
	events []*session.Event
	result agent.RunResult
	err    error
}

func (r *scriptedRuntime) Entrypoint() *agent.AgentDefinition {
	return &agent.AgentDefinition{Name: "main"}
}

func (r *scriptedRuntime) Run(_ context.Context, _ agent.RunRequest, observe func(*session.Event) error) (agent.RunResult, error) {
	for _, event := range r.events {
		if err := observe(event); err != nil {
			return agent.RunResult{}, err
		}
	}
	return r.result, r.err
}

func authenticatedRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(body))
	return request.WithContext(identity.WithIdentity(request.Context(), identity.Identity{UserID: "user_1", MutualTLS: true}))
}

func relayRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/ag-ui", strings.NewReader(body))
	return request.WithContext(identity.WithIdentity(request.Context(), identity.Identity{UserID: "user_1"}))
}

type recordingNotifier struct {
	records []notify.Record
}

func (r *recordingNotifier) Notify(record notify.Record) {
	r.records = append(r.records, record)
}

func TestAGUINotifiesCompletedRun(t *testing.T) {
	notifier := &recordingNotifier{}
	runtime := &scriptedRuntime{result: agent.RunResult{
		Text:  "答案",
		Usage: agent.RunUsage{LLMCalls: 2, PromptTokens: 10, CompletionTokens: 20, ThoughtTokens: 1},
	}}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: time.Second, Notifier: notifier})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(t, `{"threadId":"thread_1","runId":"run_1","messages":[{"id":"m1","role":"user","content":"hi"}]}`))

	if len(notifier.records) != 1 {
		t.Fatalf("records = %d, want 1", len(notifier.records))
	}
	record := notifier.records[0]
	if record.RunID != "run_1" || record.ThreadID != "thread_1" || record.UserID != "user_1" ||
		record.UserMessage != "hi" || record.Status != "completed" || record.Answer != "答案" {
		t.Fatalf("record = %+v", record)
	}
	if record.Usage.LLMCalls != 2 || record.Usage.PromptTokens != 10 ||
		record.Usage.CompletionTokens != 20 || record.Usage.ThoughtTokens != 1 {
		t.Fatalf("record usage = %+v", record.Usage)
	}
	if record.StartedAt.IsZero() || record.FinishedAt.IsZero() || record.FinishedAt.Before(record.StartedAt) {
		t.Fatalf("record timestamps = %v .. %v", record.StartedAt, record.FinishedAt)
	}
}

func TestAGUINotifiesCanceledAndFailedRuns(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"canceled", context.Canceled, "canceled"},
		{"failed", errors.New("boom"), "failed"},
	}
	for _, tc := range cases {
		notifier := &recordingNotifier{}
		handler, err := agui.New(&scriptedRuntime{err: tc.err}, agui.Config{RunTimeout: time.Second, Notifier: notifier})
		if err != nil {
			t.Fatalf("%s: New() error = %v", tc.name, err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`))

		if len(notifier.records) != 1 {
			t.Fatalf("%s: records = %d, want 1", tc.name, len(notifier.records))
		}
		record := notifier.records[0]
		if record.Status != tc.want {
			t.Fatalf("%s: status = %q, want %q", tc.name, record.Status, tc.want)
		}
		if tc.want == "failed" && !strings.Contains(record.Error, "boom") {
			t.Fatalf("%s: error = %q", tc.name, record.Error)
		}
		if tc.want == "canceled" && record.Error != "" {
			t.Fatalf("%s: canceled run must not carry an error string: %q", tc.name, record.Error)
		}
	}
}

func TestAGUINotifiesOnlyMutualTLSRuns(t *testing.T) {
	notifier := &recordingNotifier{}
	handler, err := agui.New(&scriptedRuntime{result: agent.RunResult{Text: "答案"}}, agui.Config{RunTimeout: time.Second, Notifier: notifier})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, relayRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`))

	if len(notifier.records) != 0 {
		t.Fatalf("relay-channel records = %d, want 0 (liki-web persists relay runs; callbacks are direct-connect only)", len(notifier.records))
	}
}

func TestAGUIRejectsUnsupportedCapabilitiesInsteadOfDroppingThem(t *testing.T) {
	handler, err := agui.New(&scriptedRuntime{}, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cases := map[string]string{
		"client tools":           `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}],"tools":[{"name":"x","description":"x","parameters":{}}]}`,
		"non-empty client state": `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}],"state":{"step":1}}`,
		"system history":         `{"threadId":"t","runId":"r","messages":[{"id":"s","role":"system","content":"rules"},{"id":"m","role":"user","content":"hi"}]}`,
		"multimodal":             `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":[{"type":"image","url":"https://example.test/a.png"}]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, authenticatedRequest(t, body))
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAGUIAcceptsStandardEmptyClientState(t *testing.T) {
	scripted := &scriptedRuntime{
		result: agent.RunResult{Text: "ready"},
	}
	handler, err := agui.New(scripted, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{
		"absent": "",
		"null":   `"state":null,`,
		"empty":  `"state":{},`,
	}
	for name, state := range states {
		t.Run(name, func(t *testing.T) {
			template := `{
				"threadId":"t",
				"runId":"r",
				%s
				"messages":[{"id":"m","role":"user","content":"hi"}]
			}`
			var body string
			if state == "" {
				body = fmt.Sprintf(template, "")
			} else {
				body = fmt.Sprintf(template, state)
			}
			request := authenticatedRequest(t, body)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			for _, kind := range []string{"RUN_STARTED", "TEXT_MESSAGE_CONTENT", "RUN_FINISHED"} {
				if !strings.Contains(response.Body.String(), kind) {
					t.Fatalf("missing %s in SSE body: %s", kind, response.Body.String())
				}
			}
			if strings.Contains(response.Body.String(), "unsupported_agui_feature") {
				t.Fatal("standard empty client state was rejected")
			}
		})
	}
}

func TestAGUIStreamsPartialTextDeltas(t *testing.T) {
	events := []*session.Event{
		{LLMResponse: model.LLMResponse{Partial: true, Content: &genai.Content{
			Parts: []*genai.Part{{Text: "Hello "}},
		}}},
		{LLMResponse: model.LLMResponse{Partial: true, Content: &genai.Content{
			Parts: []*genai.Part{{Text: "streaming"}},
		}}},
	}
	scripted := &scriptedRuntime{
		events: events,
		result: agent.RunResult{Text: "Hello streaming"},
	}
	handler, err := agui.New(scripted, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if got := strings.Count(body, `"type":"TEXT_MESSAGE_START"`); got != 1 {
		t.Fatalf("TEXT_MESSAGE_START count = %d, want 1", got)
	}
	if got := strings.Count(body, `"type":"TEXT_MESSAGE_CONTENT"`); got != 2 {
		t.Fatalf("TEXT_MESSAGE_CONTENT count = %d, want 2", got)
	}
	if got := strings.Count(body, `"type":"TEXT_MESSAGE_END"`); got != 1 {
		t.Fatalf("TEXT_MESSAGE_END count = %d, want 1", got)
	}
	if !strings.Contains(body, `"delta":"Hello "`) || !strings.Contains(body, `"delta":"streaming"`) {
		t.Fatalf("SSE body missing streamed deltas: %s", body)
	}
}

func TestAGUIEmitsStructuredAnswerAsTextLifecycle(t *testing.T) {
	runtime := &scriptedRuntime{
		result: agent.RunResult{Output: []byte(`{"answer":"结构化答案"}`), Text: "结构化答案"},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := authenticatedRequest(t, `{"threadId":"thread_1","runId":"run_1","messages":[{"id":"m1","role":"user","content":"hello"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	want := []string{"RUN_STARTED", "TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED"}
	last := -1
	for _, kind := range want {
		index := strings.Index(body, kind)
		if index < 0 {
			t.Fatalf("missing %s in %s", kind, body)
		}
		if index < last {
			t.Fatalf("%s appeared out of order in %s", kind, body)
		}
		last = index
	}
	if !strings.Contains(body, `"answer":"结构化答案"`) {
		t.Fatalf("RUN_FINISHED result missing authoritative answer: %s", body)
	}
	// 协议契约：RUN_FINISHED 必须携带 outcome（与中继网关一致）；缺失会被
	// AG-UI 客户端判成非成功结果。
	if !strings.Contains(body, `"outcome":{"type":"success"}`) {
		t.Fatalf("RUN_FINISHED missing success outcome: %s", body)
	}
}

func TestAGUISuppressesRunErrorOnClientDisconnect(t *testing.T) {
	runtime := &scriptedRuntime{err: context.Canceled}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`))
	if strings.Contains(response.Body.String(), "RUN_ERROR") {
		t.Fatalf("client disconnect must not emit RUN_ERROR: %s", response.Body.String())
	}
}

func TestAGUIEmitsRunErrorOnDeadline(t *testing.T) {
	runtime := &scriptedRuntime{err: context.DeadlineExceeded}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`))
	body := response.Body.String()
	if !strings.Contains(body, "RUN_ERROR") || !strings.Contains(body, "exceeded its deadline") {
		t.Fatalf("deadline must emit RUN_ERROR: %s", body)
	}
}

func TestAGUIEmitsFullToolCallSequenceWithStructuredAnswer(t *testing.T) {
	events := []*session.Event{
		{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "engine_tool_a", Args: map[string]any{"input_a": "value-a"}}},
		}}}},
		{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "call_2", Name: "engine_tool_b", Args: map[string]any{"input_b": "value-b"}}},
		}}}},
		{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: "call_1", Name: "engine_tool_a", Response: map[string]any{"result_a": "value-a"}}},
		}}}},
		{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: "call_2", Name: "engine_tool_b", Response: map[string]any{"result_b": "value-b"}}},
		}}}},
		{LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: "最终分析"}}}}},
	}
	runtime := &scriptedRuntime{
		events: events,
		result: agent.RunResult{
			Output: []byte(`{"answer":"最终分析"}`),
			Text:   "最终分析",
		},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"看事业"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	want := []string{
		"RUN_STARTED",
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
		"RUN_FINISHED",
	}
	last := -1
	for _, kind := range want {
		index := strings.Index(body, kind)
		if index < 0 {
			t.Fatalf("missing %s in SSE body:\n%s", kind, body)
		}
		if index < last {
			t.Fatalf("%s appeared out of order in SSE body:\n%s", kind, body)
		}
		last = index
	}
	if !strings.Contains(body, "engine_tool_a") {
		t.Fatalf("tool name not found in SSE body")
	}
	if !strings.Contains(body, "value-a") {
		t.Fatalf("tool result not found in SSE body")
	}
	for _, callID := range []string{"call_1", "call_2"} {
		if !strings.Contains(body, callID) {
			t.Fatalf("SSE body missing %s", callID)
		}
	}
	for _, kind := range []string{
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_RESULT", "TOOL_CALL_END",
	} {
		if got := strings.Count(body, `"type":"`+kind+`"`); got != 2 {
			t.Fatalf("%s appeared %d times, want 2", kind, got)
		}
	}
}

func TestAGUIPassesHistoryToRuntime(t *testing.T) {
	var captured agent.RunRequest
	runtime := &capturingRuntime{
		capture: &captured,
		result:  agent.RunResult{Output: []byte(`{"answer":"好的"}`), Text: "好的"},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	body := `{"threadId":"t1","runId":"r1","messages":[
		{"id":"m1","role":"user","content":"看事业"},
		{"id":"m2","role":"assistant","content":"事业方面有压力。"},
		{"id":"m3","role":"user","content":"那感情呢？"}
	]}`
	request := authenticatedRequest(t, body)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if captured.UserMessage != "那感情呢？" {
		t.Fatalf("UserMessage = %q, want latest user message", captured.UserMessage)
	}
	if len(captured.History) != 2 {
		t.Fatalf("History length = %d, want 2", len(captured.History))
	}
	if captured.History[0].Role != agent.RoleUser || captured.History[0].Content != "看事业" {
		t.Fatalf("History[0] = %+v", captured.History[0])
	}
	if captured.History[1].Role != agent.RoleAssistant || captured.History[1].Content != "事业方面有压力。" {
		t.Fatalf("History[1] = %+v", captured.History[1])
	}
	if captured.ThreadID != "t1" || captured.RunID != "r1" {
		t.Fatalf("IDs = %q/%q", captured.ThreadID, captured.RunID)
	}
}

type capturingRuntime struct {
	capture *agent.RunRequest
	result  agent.RunResult
}

type runFinishedFailureWriter struct {
	*httptest.ResponseRecorder
}

func (w *runFinishedFailureWriter) Write(body []byte) (int, error) {
	written, err := w.ResponseRecorder.Write(body)
	if err == nil && strings.Contains(string(body), `"RUN_FINISHED"`) {
		return written, errors.New("stream failed after run finished")
	}
	return written, err
}

func TestAGUIDoesNotEmitRunErrorAfterRunFinished(t *testing.T) {
	runtime := &capturingRuntime{
		capture: &agent.RunRequest{},
		result:  agent.RunResult{Text: "complete"},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	response := &runFinishedFailureWriter{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(response, authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`))

	body := response.Body.String()
	if !strings.Contains(body, "RUN_FINISHED") {
		t.Fatalf("RUN_FINISHED was not written: %s", body)
	}
	if strings.Contains(body, "RUN_ERROR") {
		t.Fatal("RUN_ERROR followed RUN_FINISHED")
	}
}

func (r *capturingRuntime) Entrypoint() *agent.AgentDefinition {
	return &agent.AgentDefinition{Name: "main"}
}

func (r *capturingRuntime) Run(_ context.Context, request agent.RunRequest, _ func(*session.Event) error) (agent.RunResult, error) {
	*r.capture = request
	return r.result, nil
}
