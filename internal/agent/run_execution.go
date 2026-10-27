package agent

import (
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/execution"
)

type runExecution struct {
	mu                sync.Mutex
	identity          execution.RunIdentity
	provenance        execution.RunProvenance
	attemptKey        string
	taskID            string
	startedAt         time.Time
	model             string
	instructionDigest string
	releaseRun        func()
	definitionName    string
	definitionVersion string
	definitionDigest  string
	nextCall          int
	activeCalls       map[string]llmCallRuntime

	llmCalls         int
	promptTokens     int
	completionTokens int
	thoughtTokens    int
}

// usage returns the aggregated model usage recorded for this run so far.
func (s *runExecution) usage() execution.RunUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return execution.RunUsage{
		LLMCalls:         s.llmCalls,
		PromptTokens:     s.promptTokens,
		CompletionTokens: s.completionTokens,
		ThoughtTokens:    s.thoughtTokens,
	}
}

type llmCallRuntime struct {
	id                string
	model             string
	agentName         string
	instructionDigest string
	startedAt         time.Time
}
