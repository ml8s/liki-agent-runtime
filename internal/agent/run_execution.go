package agent

import (
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

type runExecution struct {
	mu                sync.Mutex
	runID             domain.ID
	threadID          domain.ID
	userID            string
	startedAt         time.Time
	protocol          string
	model             string
	instructionDigest string
	traceID           string
	spanID            string
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
