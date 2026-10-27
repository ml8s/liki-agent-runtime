package agui_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/execution"
	"github.com/ml8s/agent-runtime/internal/protocol/agui"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type subagentEvent struct {
	Type          string `json:"type"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
	MessageID     string `json:"messageId,omitempty"`
	Name          string `json:"name,omitempty"`
	ParentRunID   string `json:"parentSubagentRunId,omitempty"`
	Message       string `json:"message,omitempty"`
	Code          string `json:"code,omitempty"`
	Delta         string `json:"delta,omitempty"`
	ToolCallID    string `json:"toolCallId,omitempty"`
	ToolCallName  string `json:"toolCallName,omitempty"`
}

func sseEvents(t *testing.T, body string) []subagentEvent {
	t.Helper()
	var events []subagentEvent
	for _, line := range strings.Split(body, "\n") {
		encoded, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event subagentEvent
		if err := json.Unmarshal([]byte(encoded), &event); err != nil {
			t.Fatalf("decode SSE event %q: %v", encoded, err)
		}
		events = append(events, event)
	}
	return events
}

func TestAGUIAttributesWorkerTextAndToolsToSubagent(t *testing.T) {
	events := []*session.Event{
		{
			Author: "worker",
			Branch: "main.worker",
			LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
				{Text: "worker answer"},
				{FunctionCall: &genai.FunctionCall{ID: "call_worker", Name: "test_tool"}},
			}}},
		},
		{
			Author: "worker",
			Branch: "main.worker",
			LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{ID: "call_worker", Name: "test_tool", Response: map[string]any{"ok": true}}},
			}}},
		},
		{Author: "main", Branch: "main"},
	}
	scripted := &scriptedRuntime{
		events: events,
		result: execution.RunResult{Text: "root answer"},
	}
	handler, err := agui.New(scripted, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	got := sseEvents(t, response.Body.String())
	var workerText, rootText, toolStart, toolArgs, toolResult, toolEnd []subagentEvent
	for _, event := range got {
		switch event.Type {
		case "TEXT_MESSAGE_CONTENT":
			if event.SubagentRunID != "" {
				workerText = append(workerText, event)
			} else {
				rootText = append(rootText, event)
			}
		case "TOOL_CALL_START":
			toolStart = append(toolStart, event)
		case "TOOL_CALL_ARGS":
			toolArgs = append(toolArgs, event)
		case "TOOL_CALL_RESULT":
			toolResult = append(toolResult, event)
		case "TOOL_CALL_END":
			toolEnd = append(toolEnd, event)
		}
	}
	if len(workerText) != 1 || workerText[0].Delta != "worker answer" || workerText[0].SubagentRunID == "" {
		t.Fatalf("worker text = %#v", workerText)
	}
	if len(rootText) != 1 || rootText[0].Delta != "root answer" || rootText[0].SubagentRunID != "" {
		t.Fatalf("root text = %#v", rootText)
	}
	for _, group := range [][]subagentEvent{toolStart, toolArgs, toolResult, toolEnd} {
		if len(group) != 1 || group[0].SubagentRunID != workerText[0].SubagentRunID {
			t.Fatalf("tool attribution = %#v; worker=%q", group, workerText[0].SubagentRunID)
		}
	}
}

func TestAGUIDoesNotTreatEntrypointAsSubagent(t *testing.T) {
	scripted := &scriptedRuntime{
		events: []*session.Event{{Author: "main", Branch: "main"}},
		result: execution.RunResult{Text: "root answer"},
	}
	handler, err := agui.New(scripted, agui.Config{RunTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	for _, event := range sseEvents(t, response.Body.String()) {
		if event.Type == "SUBAGENT_STARTED" || event.Type == "SUBAGENT_FINISHED" {
			t.Fatalf("entrypoint emitted subagent lifecycle: %#v", event)
		}
	}
}

func TestAGUIEmitsOfficialSubagentLifecycle(t *testing.T) {
	events := []*session.Event{
		{Author: "planner", Branch: "main.planner"},
		{Author: "worker", Branch: "main.planner.worker"},
		{Author: "worker", Branch: "main.planner.worker"},
		{Author: "planner", Branch: "main.planner"},
		{Author: "main", Branch: "main"},
	}
	scripted := &scriptedRuntime{
		events: events,
		result: execution.RunResult{Text: "complete"},
	}
	handler, err := agui.New(scripted, agui.Config{
		RunTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	events2 := sseEvents(t, response.Body.String())
	var kinds []string
	for _, event := range events2 {
		kinds = append(kinds, event.Type)
	}
	want := []string{
		"RUN_STARTED",
		"SUBAGENT_STARTED",  // planner
		"SUBAGENT_STARTED",  // worker
		"SUBAGENT_FINISHED", // worker
		"SUBAGENT_FINISHED", // planner
		"TEXT_MESSAGE_START",
		"TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_END",
		"RUN_FINISHED",
	}
	if len(kinds) < len(want) {
		t.Fatalf("events = %#v, want at least %#v", kinds, want)
	}
	for index, kind := range want {
		if kinds[index] != kind {
			t.Fatalf("event %d = %q, want %q; all=%#v", index, kinds[index], kind, kinds)
		}
	}

	started := make([]subagentEvent, 0, 2)
	finished := make([]subagentEvent, 0, 2)
	for _, event := range events2 {
		switch event.Type {
		case "SUBAGENT_STARTED":
			started = append(started, event)
		case "SUBAGENT_FINISHED":
			finished = append(finished, event)
		}
	}
	if len(started) != 2 || len(finished) != 2 {
		t.Fatalf("subagent events = %d started / %d finished", len(started), len(finished))
	}
	if started[0].Name != "planner" || started[1].Name != "worker" {
		t.Fatalf("started names = %#v", started)
	}
	if started[1].ParentRunID != started[0].SubagentRunID {
		t.Fatalf("worker parent = %q, want planner %q", started[1].ParentRunID, started[0].SubagentRunID)
	}
	if finished[0].SubagentRunID != started[1].SubagentRunID ||
		finished[1].SubagentRunID != started[0].SubagentRunID {
		t.Fatalf("finished order = %#v / %#v", started, finished)
	}
}

func TestAGUIEmitsSubagentErrorBeforeRunError(t *testing.T) {
	scripted := &scriptedRuntime{
		events: []*session.Event{{Author: "worker", Branch: "main.worker"}},
		err:    context.DeadlineExceeded,
	}
	handler, err := agui.New(scripted, agui.Config{
		RunTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(t, `{"threadId":"t","runId":"r","messages":[{"id":"m","role":"user","content":"hi"}]}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	events := sseEvents(t, response.Body.String())
	subagentError := -1
	runError := -1
	for index, event := range events {
		switch event.Type {
		case "SUBAGENT_ERROR":
			subagentError = index
			if event.Code != "runtime_timeout" {
				t.Fatalf("subagent error code = %q", event.Code)
			}
		case "RUN_ERROR":
			runError = index
			if event.Code != "runtime_timeout" {
				t.Fatalf("run error code = %q", event.Code)
			}
		}
	}
	if subagentError < 0 || runError < 0 || subagentError > runError {
		t.Fatalf("events=%#v subagentError=%d runError=%d", events, subagentError, runError)
	}
}
