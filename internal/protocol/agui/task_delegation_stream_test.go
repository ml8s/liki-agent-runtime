package agui_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/execution"
	"github.com/ml8s/liki-agent-runtime/internal/protocol/agui"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Task-mode sub-agents inherit the parent's branch (usually empty at the
// root), so their events carry the sub-agent author with an empty branch.
// Attribution must fall back to the author instead of treating the events as
// root output.
func TestAGUIAttributesTaskDelegatedWorkerByAuthor(t *testing.T) {
	events := []*session.Event{
		{
			Author: "worker",
			LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{
				{Text: "worker answer"},
			}}},
		},
		{Author: "main"},
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
	var workerText, rootText []subagentEvent
	subagentStarted := false
	subagentFinished := false
	for _, event := range got {
		switch event.Type {
		case "SUBAGENT_STARTED":
			if event.Name == "worker" {
				subagentStarted = true
			}
		case "SUBAGENT_FINISHED":
			subagentFinished = true
		case "TEXT_MESSAGE_CONTENT":
			if event.SubagentRunID != "" {
				workerText = append(workerText, event)
			} else {
				rootText = append(rootText, event)
			}
		}
	}
	if !subagentStarted || !subagentFinished {
		t.Fatalf("subagent lifecycle missing: started=%v finished=%v events=%#v", subagentStarted, subagentFinished, got)
	}
	if len(workerText) != 1 || workerText[0].Delta != "worker answer" {
		t.Fatalf("worker text = %#v", workerText)
	}
	if len(rootText) != 1 || rootText[0].Delta != "root answer" {
		t.Fatalf("root text = %#v", rootText)
	}
}
