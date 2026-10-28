package execution

import (
	"errors"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/domain"
)

func TestNormalizeLLMCall(t *testing.T) {
	t.Parallel()
	input := LLMCall{
		ID:        " call_1 ",
		AgentName: " coordinator ",
		Model:     " test-model ",
		ErrorCode: " runtime_timeout ",
	}
	normalized := NormalizeLLMCall(input)
	if normalized.ID != "call_1" || normalized.AgentName != "coordinator" || normalized.Model != "test-model" || normalized.ErrorCode != domain.CodeRuntimeTimeout {
		t.Fatalf("normalized call = %+v", normalized)
	}
	if input.ID != " call_1 " || input.AgentName != " coordinator " || input.Model != " test-model " || input.ErrorCode != " runtime_timeout " {
		t.Fatalf("input call = %+v, want unchanged", input)
	}
}

func TestValidateLLMCall(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	valid := LLMCall{
		ID: "call_1", RunID: "run_1", ThreadID: "thread_1", UserID: "user_1",
		AgentName: "coordinator", Model: "test-model", StartedAt: started,
	}
	if err := ValidateLLMCall(valid); err != nil {
		t.Fatalf("ValidateLLMCall() error = %v", err)
	}

	testCases := []struct {
		name     string
		mutate   func(*LLMCall)
		wantCode string
	}{
		{name: "missing id", mutate: func(c *LLMCall) { c.ID = "" }, wantCode: domain.CodeLLMCallIDRequired},
		{name: "missing run", mutate: func(c *LLMCall) { c.RunID = "" }, wantCode: domain.CodeRunIDRequired},
		{name: "missing thread", mutate: func(c *LLMCall) { c.ThreadID = "" }, wantCode: domain.CodeThreadIDRequired},
		{name: "missing user", mutate: func(c *LLMCall) { c.UserID = "" }, wantCode: domain.CodeUserIDRequired},
		{name: "missing agent", mutate: func(c *LLMCall) { c.AgentName = "" }, wantCode: domain.CodeAgentNameRequired},
		{name: "missing model", mutate: func(c *LLMCall) { c.Model = "" }, wantCode: domain.CodeModelRequired},
		{name: "missing start time", mutate: func(c *LLMCall) { c.StartedAt = time.Time{} }, wantCode: domain.CodeLLMCallStartedAtRequired},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			call := valid
			testCase.mutate(&call)
			err := ValidateLLMCall(call)
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != testCase.wantCode {
				t.Fatalf("ValidateLLMCall() error = %v, want code %s", err, testCase.wantCode)
			}
		})
	}
}

func TestLLMCallLifecycle(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	call := LLMCall{ID: "call_1", StartedAt: started}

	completed := CompleteLLMCall(call, LLMTokenUsage{PromptTokens: 10, CompletionTokens: 4, ThoughtTokens: 1, TotalTokens: 15}, finished)
	if completed.Status != LLMCallCompleted || completed.DurationMS != 2000 || completed.TotalTokens != 15 {
		t.Fatalf("completed call = %+v", completed)
	}

	failed := FailLLMCall(call, domain.NewError(domain.CodeRuntimeTimeout, "runtime timeout", nil), finished)
	if failed.Status != LLMCallFailed || failed.ErrorCode != domain.CodeRuntimeTimeout || failed.ErrorMessage != "runtime timeout" {
		t.Fatalf("domain failed call = %+v", failed)
	}

	genericFailed := FailLLMCall(call, errors.New("network failed"), finished)
	if genericFailed.ErrorCode != domain.CodeLLMCallFailed || genericFailed.ErrorMessage != "LLM call failed" {
		t.Fatalf("generic failed call = %+v", genericFailed)
	}
}

func TestPureLLMCallTransitionsDoNotMutateInput(t *testing.T) {
	t.Parallel()
	startedAt := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	started := LLMCall{ID: "call_1", RunID: "run_1", ThreadID: "thread_1", UserID: "user_1", AgentName: "main", Model: "model", Status: LLMCallRunning, StartedAt: startedAt}

	completed := CompleteLLMCall(started, LLMTokenUsage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}, startedAt.Add(time.Second))
	if completed.Status != LLMCallCompleted || completed.TotalTokens != 5 {
		t.Fatalf("completed = %#v", completed)
	}
	if started.Status != LLMCallRunning || started.TotalTokens != 0 {
		t.Fatalf("input after complete = %#v, want unchanged", started)
	}

	failed := FailLLMCall(started, errors.New("failed"), startedAt.Add(time.Second))
	if failed.Status != LLMCallFailed || failed.ErrorCode != domain.CodeLLMCallFailed {
		t.Fatalf("failed = %#v", failed)
	}
	if started.Status != LLMCallRunning || started.ErrorCode != "" {
		t.Fatalf("input after fail = %#v, want unchanged", started)
	}
}
