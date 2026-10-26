package agent

import (
	"sync"
	"time"
)

type runExecution struct {
	mu                sync.Mutex
	identity          RunIdentity
	provenance        RunProvenance
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
func (s *runExecution) usage() RunUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RunUsage{
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
