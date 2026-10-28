package agent

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	adksqlite "github.com/ml8s/agent-runtime/internal/adksession/sqlite"
	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/ml8s/agent-runtime/internal/audit"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// cyclingTurns is the model script for one complete run: one function call
// (exercising the MCP tool pipeline and audit) followed by one final text
// answer. Every run consumes exactly two turns, so concurrent runs stay
// aligned on the cycling script.
func cyclingTurns() []*model.LLMResponse {
	return []*model.LLMResponse{
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "fc_tool", Name: "test_tool", Args: map[string]any{"q": "x"}}},
		}}},
		{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "final answer"}}}},
	}
}

type cyclingLLM struct {
	mu    sync.Mutex
	turn  int
	turns []*model.LLMResponse
}

func (f *cyclingLLM) Name() string { return "cycling-model" }

func (f *cyclingLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		f.mu.Lock()
		index := f.turn
		f.turn++
		f.mu.Unlock()
		yield(f.turns[index%len(f.turns)], nil)
	}
}

// benchHistory builds a representative mid-length thread so every run
// exercises the multi-turn seeding and contents assembly at a realistic size.
func benchHistory() []execution.Message {
	history := make([]execution.Message, 0, 20)
	for index := 0; index < 10; index++ {
		history = append(history,
			execution.Message{Role: execution.RoleUser, Content: fmt.Sprintf("第 %d 问：请结合盘面结论给出该阶段的要点与依据。", index+1)},
			execution.Message{Role: execution.RoleAssistant, Content: fmt.Sprintf("第 %d 答结论摘要，含盘面要点、依据与不确定项说明。", index+1)},
		)
	}
	return history
}

// BenchmarkRuntimeRun measures the end-to-end runtime path per run: request
// validation, multi-turn history seeding, ADK flow (model call + MCP tool +
// function-response round), streaming event projection, and audit recording
// through the single-writer recorder. It isolates runtime framework overhead
// from model and MCP network time.
func BenchmarkRuntimeRun(b *testing.B) {
	agentDeployment := plainTestDeployment(b)
	events := &recordingAudit{}
	llm := &cyclingLLM{turns: cyclingTurns()}
	runtime := newTestRuntime(b, agentDeployment, events, llm)
	history := benchHistory()
	ctx := context.Background()

	// benchRunSeq is process-global and never resets: benchmark closures are
	// re-invoked for calibration rounds with increasing b.N, and the runtime's
	// completed-run registry would flag a reset sequence as duplicates.
	var benchRunSeq atomic.Int64

	runConcurrent := func(b *testing.B, workers int) {
		b.ReportAllocs()
		var cursor atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for {
					if cursor.Add(1) > int64(b.N) {
						return
					}
					n := benchRunSeq.Add(1)
					request := execution.RunRequest{
						RunID:       fmt.Sprintf("run_bench_%d", n),
						ThreadID:    fmt.Sprintf("thread_bench_%d", n),
						UserID:      "user_bench",
						UserMessage: "当前问题",
						History:     history,
					}
					if _, err := runtime.Run(ctx, request, nil); err != nil {
						b.Error(err)
						return
					}
				}
			}()
		}
		close(start)
		wg.Wait()
	}

	for _, workers := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			runConcurrent(b, workers)
		})
	}
}

// Compile-time guard: the benchmark exercises the real audit event type.
var _ = audit.EventDelegationStarted

// BenchmarkRuntimeRunSQLiteSessions measures the production composition shape:
// the durable session service is SQLite-backed exactly as in the deployed
// process, so this benchmark exposes session-storage I/O that the in-memory
// BenchmarkRuntimeRun cannot see. It keeps the same 20-message history, tool
// call, and in-memory audit recorder so the only variable is the session store.
func BenchmarkRuntimeRunSQLiteSessions(b *testing.B) {
	store, err := adksqlite.Open(filepath.Join(b.TempDir(), "sessions.db"))
	if err != nil {
		b.Fatalf("open sqlite session store: %v", err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Fatalf("close sqlite session store: %v", err)
		}
	})
	agentDeployment := plainTestDeployment(b)
	events := &recordingAudit{}
	llm := &cyclingLLM{turns: cyclingTurns()}
	runtime := newTestRuntimeWithSessions(b, agentDeployment, events, llm, store.SessionService())
	history := benchHistory()
	ctx := context.Background()

	var benchRunSeq atomic.Int64
	b.Run("workers=1", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			n := benchRunSeq.Add(1)
			request := execution.RunRequest{
				RunID:       fmt.Sprintf("run_sql_bench_%d", n),
				ThreadID:    fmt.Sprintf("thread_sql_bench_%d", n),
				UserID:      "user_bench",
				UserMessage: "当前问题",
				History:     history,
			}
			if _, err := runtime.Run(ctx, request, nil); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
