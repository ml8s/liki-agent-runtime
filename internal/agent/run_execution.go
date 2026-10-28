package agent

import (
	"sync"
	"time"

	"github.com/ml8s/agent-runtime/internal/execution"
)

type runExecution struct {
	// mu guards the mutable call bookkeeping below (nextCall, activeCalls,
	// and the usage counters). It is never held together with the attempt
	// registry lock.
	mu sync.Mutex

	// The fields from identity through releaseRun are immutable after the
	// scope is constructed: registration publishes the pointer only after
	// every field is final, so no other lock ever protects them.
	identity          execution.RunIdentity
	provenance        execution.RunProvenance
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
