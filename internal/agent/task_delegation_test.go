package agent

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// taskDelegationDeployment builds the standard ADK agent-as-tool shape: a
// plain-text chat coordinator with one task-mode sub-agent. It is the same
// shape the natal expert uses for bazi/ziwei.
func taskDelegationDeployment(t *testing.T) *Deployment {
	t.Helper()
	deployment := plainTestDeployment(t)
	root := &deployment.Spec.Agents[0]
	root.Name = "coordinator"
	root.SubAgents = []AgentReference{{Name: "worker"}}
	worker := *root
	worker.Name = "worker"
	worker.Description = "generic task worker"
	worker.Mode = AgentModeTask
	worker.SubAgents = nil
	deployment.Spec.Agents = append(deployment.Spec.Agents, worker)
	if err := deployment.validate(); err != nil {
		t.Fatalf("validate task delegation deployment: %v", err)
	}
	return deployment
}

// taskDelegationLLM scripts one model invocation per turn: coordinator
// delegates to the task sub-agent, the task agent finishes via finish_task,
// and the coordinator synthesises the final answer from the delegated result.
func taskDelegationLLM() *turnBasedLLM {
	return &turnBasedLLM{turns: []*model.LLMResponse{
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "fc_worker_1", Name: "worker", Args: map[string]any{"request": "complete the task"},
		}}}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "fc_finish_1", Name: "finish_task", Args: map[string]any{"result": "worker summary"},
		}}}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "coordinator final answer"}}}},
	}}
}

func functionResponseOf(events []*session.Event, name string) map[string]any {
	for _, event := range events {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part == nil || part.FunctionResponse == nil || part.FunctionResponse.Name != name {
				continue
			}
			return part.FunctionResponse.Response
		}
	}
	return nil
}

func TestRuntimeRunsTaskDelegationEndToEnd(t *testing.T) {
	deployment := taskDelegationDeployment(t)
	events := &recordingAudit{}
	runtime := newTestRuntime(t, deployment, events, taskDelegationLLM())

	var observed []*session.Event
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_task_delegation", ThreadID: "thread_task_delegation", UserID: "user_1",
		UserMessage: "delegate this",
	}, func(event *session.Event) error {
		observed = append(observed, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "coordinator final answer" {
		t.Fatalf("Text = %q, want coordinator final answer", result.Text)
	}
	// The final answer must be authored by the coordinator, not by the task
	// sub-agent leaking its scripted turn into the run result.
	finalAuthor := ""
	for _, event := range observed {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.Text == "coordinator final answer" {
				finalAuthor = event.Author
			}
		}
	}
	if finalAuthor != "coordinator" {
		t.Fatalf("final answer author = %q, want coordinator", finalAuthor)
	}
	// The task agent's finish_task output must be returned to the coordinator
	// as the synthesised function response of the task tool call.
	if response := functionResponseOf(observed, "worker"); response["result"] != "worker summary" {
		t.Fatalf("task tool response = %#v, want worker summary", response)
	}
	// finish_task must execute through the standard tool pipeline.
	if response := functionResponseOf(observed, "finish_task"); response["result"] != "Task completed." {
		t.Fatalf("finish_task response = %#v, want success", response)
	}
	if got := len(events.eventsOfType(audit.EventDelegationStarted)); got != 1 {
		t.Fatalf("delegation started events = %d, want 1", got)
	}
	if got := len(events.eventsOfType(audit.EventDelegationCompleted)); got != 1 {
		t.Fatalf("delegation completed events = %d, want 1", got)
	}
	if got := len(events.eventsOfType(audit.EventDelegationFailed)); got != 0 {
		t.Fatalf("delegation failed events = %d, want 0", got)
	}
	// ADK's framework delegation tools (the task tool named after the
	// sub-agent and the task agent's finish_task) are not MCP tools; they must
	// not be audited as MCP tool calls, exactly like transfer_to_agent.
	for _, eventType := range []audit.EventType{
		audit.EventToolCallStarted,
		audit.EventToolCallCompleted,
		audit.EventToolCallFailed,
	} {
		for _, event := range events.eventsOfType(eventType) {
			if event.ToolName == "worker" || event.ToolName == "finish_task" {
				t.Fatalf("framework delegation tool %q produced %s evidence", event.ToolName, eventType)
			}
		}
	}
}

// twoTaskDelegationDeployment builds the natal shape at its smallest: one chat
// coordinator with TWO task-mode sub-agents (bazi/ziwei).
func twoTaskDelegationDeployment(t *testing.T) *Deployment {
	t.Helper()
	deployment := plainTestDeployment(t)
	root := &deployment.Spec.Agents[0]
	root.Name = "coordinator"
	root.SubAgents = []AgentReference{{Name: "worker_a"}, {Name: "worker_b"}}
	for _, name := range []string{"worker_a", "worker_b"} {
		worker := *root
		worker.Name = name
		worker.Description = "generic task worker " + name
		worker.Mode = AgentModeTask
		worker.SubAgents = nil
		deployment.Spec.Agents = append(deployment.Spec.Agents, worker)
	}
	if err := deployment.validate(); err != nil {
		t.Fatalf("validate two-task delegation deployment: %v", err)
	}
	return deployment
}

// markerKeyedLLM routes responses by request-content markers instead of call
// order, so two concurrently running task sub-agents stay deterministic.
type markerKeyedLLM struct {
	mu       sync.Mutex
	requests []string
	markers  []markerResponse
	fallback *model.LLMResponse

	// 并发探针：worker 调用在途计数（marker 命中 task a/task b 时）。
	activeWorkers int
	maxActive     int
	workerOverlap bool
}

type markerResponse struct {
	markers  []string
	response *model.LLMResponse
	worker   bool
}

func (f *markerKeyedLLM) Name() string { return "fake-model" }

func (f *markerKeyedLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		fingerprint := requestFingerprint(req)
		f.mu.Lock()
		f.requests = append(f.requests, fingerprint)
		response := f.fallback
		isWorker := false
		for _, candidate := range f.markers {
			matched := true
			for _, marker := range candidate.markers {
				if !strings.Contains(fingerprint, marker) {
					matched = false
					break
				}
			}
			if matched {
				response = candidate.response
				isWorker = candidate.worker
				break
			}
		}
		if isWorker {
			f.activeWorkers++
			if f.activeWorkers > f.maxActive {
				f.maxActive = f.activeWorkers
			}
			if f.activeWorkers > 1 {
				f.workerOverlap = true
			}
		}
		f.mu.Unlock()
		if isWorker {
			// 拉长在途窗口，让并发/串行可判别。
			time.Sleep(20 * time.Millisecond)
			f.mu.Lock()
			f.activeWorkers--
			f.mu.Unlock()
		}
		if response != nil {
			yield(response, nil)
		}
	}
}

func requestFingerprint(req *model.LLMRequest) string {
	var builder strings.Builder
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			builder.WriteString(part.Text)
			if part.FunctionCall != nil {
				builder.WriteString(" call:" + part.FunctionCall.Name)
				fmt.Fprintf(&builder, " args:%v", part.FunctionCall.Args)
			}
			if part.FunctionResponse != nil {
				builder.WriteString(" response:" + part.FunctionResponse.Name)
				fmt.Fprintf(&builder, " payload:%v", part.FunctionResponse.Response)
			}
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func functionCallResponse(calls ...*genai.Part) *model.LLMResponse {
	return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: calls}}
}

// TestRuntimeRunsTwoTaskDelegationsInOneTurn pins the framework behaviour the
// natal 合参 path depends on: when the coordinator emits BOTH task calls in one
// model response, both sub-agents run, both results return, and the run
// completes with consistent audit evidence (run with -race).
func TestRuntimeRunsTwoTaskDelegationsInOneTurn(t *testing.T) {
	deployment := twoTaskDelegationDeployment(t)
	events := &recordingAudit{}
	llm := &markerKeyedLLM{
		markers: []markerResponse{
			// Coordinator's final turn: both task responses joined. Checked
			// first because the coordinator's history also echoes the task
			// call arguments ("task a"/"task b").
			{markers: []string{"summary a", "summary b"}, response: functionCallResponse(&genai.Part{Text: "both summaries integrated"})},
			{markers: []string{"task a"}, worker: true, response: functionCallResponse(&genai.Part{FunctionCall: &genai.FunctionCall{
				ID: "fc_a_finish", Name: "finish_task", Args: map[string]any{"result": "summary a"},
			}})},
			{markers: []string{"task b"}, worker: true, response: functionCallResponse(&genai.Part{FunctionCall: &genai.FunctionCall{
				ID: "fc_b_finish", Name: "finish_task", Args: map[string]any{"result": "summary b"},
			}})},
		},
		// Coordinator's first turn: both task calls in ONE model response.
		fallback: functionCallResponse(
			&genai.Part{FunctionCall: &genai.FunctionCall{ID: "fc_worker_a", Name: "worker_a", Args: map[string]any{"request": "task a"}}},
			&genai.Part{FunctionCall: &genai.FunctionCall{ID: "fc_worker_b", Name: "worker_b", Args: map[string]any{"request": "task b"}}},
		),
	}
	runtime := newTestRuntime(t, deployment, events, llm)

	var observed []*session.Event
	result, err := runtime.Run(context.Background(), RunRequest{
		RunID: "run_two_task_delegation", ThreadID: "thread_two_task_delegation", UserID: "user_1",
		UserMessage: "delegate both",
	}, func(event *session.Event) error {
		observed = append(observed, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "both summaries integrated" {
		t.Fatalf("Text = %q, want both summaries integrated", result.Text)
	}
	if response := functionResponseOf(observed, "worker_a"); response["result"] != "summary a" {
		t.Fatalf("worker_a task response = %#v, want summary a", response)
	}
	if response := functionResponseOf(observed, "worker_b"); response["result"] != "summary b" {
		t.Fatalf("worker_b task response = %#v, want summary b", response)
	}
	finishResponses := 0
	for _, event := range observed {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionResponse != nil && part.FunctionResponse.Name == "finish_task" {
				finishResponses++
			}
		}
	}
	if finishResponses != 2 {
		t.Fatalf("finish_task responses = %d, want 2 (both workers complete)", finishResponses)
	}
	if got := len(events.eventsOfType(audit.EventDelegationStarted)); got != 2 {
		t.Fatalf("delegation started events = %d, want 2", got)
	}
	if got := len(events.eventsOfType(audit.EventDelegationCompleted)); got != 2 {
		t.Fatalf("delegation completed events = %d, want 2", got)
	}
	if got := len(events.eventsOfType(audit.EventDelegationFailed)); got != 0 {
		t.Fatalf("delegation failed events = %d, want 0", got)
	}
	for _, eventType := range []audit.EventType{
		audit.EventToolCallStarted,
		audit.EventToolCallCompleted,
		audit.EventToolCallFailed,
	} {
		for _, event := range events.eventsOfType(eventType) {
			if event.ToolName == "worker_a" || event.ToolName == "worker_b" || event.ToolName == "finish_task" {
				t.Fatalf("framework delegation tool %q produced %s evidence", event.ToolName, eventType)
			}
		}
	}

	// 机制事实：ADK 对 task 委派顺序派发（chat wrapper 注释："Sequential
	// dispatch means at most one unresolved task delegation at a time"），并在
	// task 工具描述里注入"不要并行调用"。即使模型同轮发出两个委派调用，两个
	// 子代理也不重叠——串行是框架行为，不需要 instruction 散文。若未来框架改为
	// 并行派发，本断言失败，需重新评估指令、runner 与审计。
	llm.mu.Lock()
	overlap, maxActive := llm.workerOverlap, llm.maxActive
	llm.mu.Unlock()
	if overlap || maxActive != 1 {
		t.Fatalf("task delegations overlapped (maxActive=%d, overlap=%v); framework no longer dispatches task delegations sequentially", maxActive, overlap)
	}
}
