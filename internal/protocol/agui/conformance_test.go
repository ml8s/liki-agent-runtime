package agui_test

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ml8s/agentgo/internal/execution"
	"github.com/ml8s/agentgo/internal/protocol/agui"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const testTimeout = time.Second

// decodeAGUIEventStream parses the wire emitted by the official SSE writer and
// revalidates every frame through the official AG-UI Go event model.
func decodeAGUIEventStream(t *testing.T, body string) []map[string]any {
	t.Helper()

	var events []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decode AG-UI frame %q: %v", line, err)
		}
		raw := []byte(strings.TrimPrefix(line, "data: "))
		officialEvent, err := aguievents.EventFromJSON(raw)
		if err != nil {
			t.Fatalf("official AG-UI SDK decode frame %q: %v", line, err)
		}
		if err := officialEvent.Validate(); err != nil {
			t.Fatalf("official AG-UI SDK validate frame %q: %v", line, err)
		}
		if got, want := string(officialEvent.Type()), event["type"]; got != want {
			t.Fatalf("official event type = %q, want %q", got, want)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan AG-UI stream: %v", err)
	}
	return events
}

func eventType(event map[string]any) string {
	value, _ := event["type"].(string)
	return value
}

func requireAGUIEventSequence(t *testing.T, events []map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, eventType(event))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence = %v, want %v\nbody events: %#v", got, want, events)
	}
}

func requireCommonAGUIEnvelope(t *testing.T, events []map[string]any, threadID, runID string) {
	t.Helper()
	for index, event := range events {
		if event["type"] == "" {
			t.Fatalf("event[%d] lacks type: %#v", index, event)
		}
		if _, ok := event["timestamp"].(float64); !ok {
			t.Fatalf("event[%d] lacks numeric timestamp: %#v", index, event)
		}
		switch eventType(event) {
		case "RUN_STARTED", "RUN_FINISHED", "RUN_ERROR":
			if event["threadId"] != threadID || event["runId"] != runID {
				t.Fatalf("%s event[%d] identity = %#v, want thread %q run %q",
					eventType(event), index, event, threadID, runID)
			}
		}
	}
}

func TestAGUITextRunConformance(t *testing.T) {
	events := []*session.Event{{
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{Text: "structured answer"},
		}}},
	}}
	runtime := &scriptedRuntime{
		events: events,
		result: execution.RunResult{
			Output: []byte(`{"answer":"structured answer"}`),
			Text:   "structured answer",
			Usage: execution.RunUsage{
				LLMCalls:          1,
				PromptTokens:      7,
				CompletionTokens:  3,
				ThoughtTokens:     1,
				TotalTokens:       11,
				CachedInputTokens: 2,
				Models: []execution.ModelTokenUsage{{
					Provider:          "test-provider",
					Model:             "test-model",
					LLMCalls:          1,
					PromptTokens:      7,
					CompletionTokens:  3,
					ThoughtTokens:     1,
					TotalTokens:       11,
					CachedInputTokens: 2,
				}},
			},
		},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: testTimeout})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := authenticatedRequest(t, `{"threadId":"thread_1","runId":"run_1","messages":[{"id":"m1","role":"user","content":"hello"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	decoded := decodeAGUIEventStream(t, response.Body.String())
	requireAGUIEventSequence(t, decoded, []string{
		"RUN_STARTED", "TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_END", "RUN_FINISHED",
	})
	requireCommonAGUIEnvelope(t, decoded, "thread_1", "run_1")

	started := decoded[0]
	if started["threadId"] != "thread_1" || started["runId"] != "run_1" {
		t.Fatalf("RUN_STARTED identity = %#v", started)
	}

	finished := decoded[len(decoded)-1]
	outcome, _ := finished["outcome"].(map[string]any)
	if outcome == nil || outcome["type"] != "success" {
		t.Fatalf("RUN_FINISHED outcome = %#v, want success without pending calls", finished["outcome"])
	}
	result, ok := finished["result"].(map[string]any)
	if !ok || result["answer"] != "structured answer" {
		t.Fatalf("RUN_FINISHED result = %#v, want structured answer", finished["result"])
	}

	usage, ok := finished["usage"].([]any)
	if !ok || len(usage) != 1 {
		t.Fatalf("RUN_FINISHED usage = %#v, want one official TokenUsage", finished["usage"])
	}
	entry, _ := usage[0].(map[string]any)
	want := map[string]any{
		"provider":          "test-provider",
		"model":             "test-model",
		"inputTokens":       float64(7),
		"outputTokens":      float64(3),
		"totalTokens":       float64(11),
		"reasoningTokens":   float64(1),
		"cachedInputTokens": float64(2),
	}
	for key, value := range want {
		if entry[key] != value {
			t.Fatalf("usage[%q] = %#v, want %#v; entry=%#v", key, entry[key], value, entry)
		}
	}
}

func TestAGUISuccessRunPreservesPendingToolCallIDs(t *testing.T) {
	events := []*session.Event{{
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{
				ID:   "call_frontend",
				Name: "render_ui",
				Args: map[string]any{"title": "agentgo"},
			}},
		}}},
	}}
	runtime := &scriptedRuntime{
		events: events,
		result: execution.RunResult{
			Text: "done",
		},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: testTimeout})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := authenticatedRequest(t, `{"threadId":"thread_1","runId":"run_1","messages":[{"id":"m1","role":"user","content":"done"}],"tools":[{"name":"render_ui","description":"Render a UI.","parameters":{"type":"object","properties":{"title":{"type":"string"}}}}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	decoded := decodeAGUIEventStream(t, response.Body.String())
	requireAGUIEventSequence(t, decoded, []string{
		"RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END",
		"RUN_FINISHED",
	})

	finished := decoded[len(decoded)-1]
	outcome, _ := finished["outcome"].(map[string]any)
	if outcome == nil || outcome["type"] != "success" {
		t.Fatalf("RUN_FINISHED outcome = %#v, want success", finished["outcome"])
	}
	pending, _ := outcome["pendingToolCallIds"].([]any)
	if len(pending) != 1 || pending[0] != "call_frontend" {
		t.Fatalf("pendingToolCallIds = %#v, want [call_frontend]", outcome["pendingToolCallIds"])
	}
}

func TestAGUIInterruptRunHasNoPendingToolCallIDs(t *testing.T) {
	events := []*session.Event{{
		LongRunningToolIDs: []string{"call_approval"},
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "call_approval", Name: "approve", Args: map[string]any{"answer": true}}},
		}}},
	}}
	runtime := &scriptedRuntime{
		events: events,
		result: execution.RunResult{
			PendingClientTools: []execution.PendingClientTool{{
				ToolCallID: "call_approval",
				ToolName:   "approve",
				Args:       []byte(`{"answer":true}`),
			}},
		},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: testTimeout})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := authenticatedRequest(t, `{"threadId":"thread_1","runId":"run_1","messages":[{"id":"m1","role":"user","content":"continue"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	decoded := decodeAGUIEventStream(t, response.Body.String())
	requireAGUIEventSequence(t, decoded, []string{
		"RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"MESSAGES_SNAPSHOT", "RUN_FINISHED",
	})
	finished := decoded[len(decoded)-1]
	outcome, _ := finished["outcome"].(map[string]any)
	if outcome == nil || outcome["type"] != "interrupt" {
		t.Fatalf("RUN_FINISHED outcome = %#v, want interrupt", finished["outcome"])
	}
	if _, exists := outcome["pendingToolCallIds"]; exists {
		t.Fatalf("interrupt outcome must not contain pendingToolCallIds: %#v", outcome)
	}
}

func TestAGUISuccessRunOmitsBackendPendingToolCallIDs(t *testing.T) {
	events := []*session.Event{{
		LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{
				ID:   "call_backend",
				Name: "backend_tool",
				Args: map[string]any{"query": "agentgo"},
			}},
		}}},
	}}
	runtime := &scriptedRuntime{
		events: events,
		result: execution.RunResult{Text: "done"},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: testTimeout})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := authenticatedRequest(t, `{"threadId":"thread_1","runId":"run_1","messages":[{"id":"m1","role":"user","content":"search"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	decoded := decodeAGUIEventStream(t, response.Body.String())
	requireAGUIEventSequence(t, decoded, []string{
		"RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END",
		"RUN_FINISHED",
	})
	finished := decoded[len(decoded)-1]
	outcome, _ := finished["outcome"].(map[string]any)
	if outcome == nil || outcome["type"] != "success" {
		t.Fatalf("RUN_FINISHED outcome = %#v, want success", finished["outcome"])
	}
	if _, exists := outcome["pendingToolCallIds"]; exists {
		t.Fatalf("backend tool call must not enter pendingToolCallIds: %#v", outcome)
	}
}

func TestAGUIRunWithoutModelCallsOmitsUsage(t *testing.T) {
	runtime := &scriptedRuntime{}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: testTimeout})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := authenticatedRequest(t, `{"threadId":"thread_1","runId":"run_1","messages":[{"id":"m1","role":"user","content":"done"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	decoded := decodeAGUIEventStream(t, response.Body.String())
	requireAGUIEventSequence(t, decoded, []string{"RUN_STARTED", "RUN_FINISHED"})
	finished := decoded[len(decoded)-1]
	if _, exists := finished["usage"]; exists {
		t.Fatalf("RUN_FINISHED usage = %#v, want omitted without model calls", finished["usage"])
	}
}

func TestAGUITokenUsagePreservesModelBoundaries(t *testing.T) {
	runtime := &scriptedRuntime{
		result: execution.RunResult{
			Text: "done",
			Usage: execution.RunUsage{
				LLMCalls: 2,
				Models: []execution.ModelTokenUsage{
					{
						Provider:         "provider-a",
						Model:            "model-a",
						LLMCalls:         1,
						PromptTokens:     7,
						CompletionTokens: 3,
						TotalTokens:      11,
					},
					{
						Provider:         "provider-b",
						Model:            "model-b",
						LLMCalls:         1,
						PromptTokens:     5,
						CompletionTokens: 4,
						TotalTokens:      9,
					},
				},
			},
		},
	}
	handler, err := agui.New(runtime, agui.Config{RunTimeout: testTimeout})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := authenticatedRequest(t, `{"threadId":"thread_1","runId":"run_1","messages":[{"id":"m1","role":"user","content":"done"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	decoded := decodeAGUIEventStream(t, response.Body.String())
	finished := decoded[len(decoded)-1]
	usage, _ := finished["usage"].([]any)
	if len(usage) != 2 {
		t.Fatalf("usage = %#v, want two model-bound entries", finished["usage"])
	}
	first, _ := usage[0].(map[string]any)
	second, _ := usage[1].(map[string]any)
	if first["model"] != "model-a" || second["model"] != "model-b" {
		t.Fatalf("usage models = %#v/%#v, want model-a/model-b", first, second)
	}
	if first["provider"] != "provider-a" || second["provider"] != "provider-b" {
		t.Fatalf("usage providers = %#v/%#v, want provider-a/provider-b", first, second)
	}
}
