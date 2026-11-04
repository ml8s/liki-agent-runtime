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

// runExecutionParams is the immutable construction contract shared by the
// native Run path and externally driven protocol audit paths.
type runExecutionParams struct {
	identity          execution.RunIdentity
	provenance        execution.RunProvenance
	taskID            string
	model             string
	instructionDigest string
	definitionName    string
	definitionVersion string
	definitionDigest  string
	startedAt         time.Time
	releaseRun        func()
}

func newRunExecution(params runExecutionParams) *runExecution {
	return &runExecution{
		identity:          params.identity,
		provenance:        params.provenance,
		taskID:            params.taskID,
		model:             params.model,
		instructionDigest: params.instructionDigest,
		releaseRun:        params.releaseRun,
		definitionName:    params.definitionName,
		definitionVersion: params.definitionVersion,
		definitionDigest:  params.definitionDigest,
		startedAt:         params.startedAt,
		activeCalls:       make(map[string]llmCallRuntime),
	}
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
