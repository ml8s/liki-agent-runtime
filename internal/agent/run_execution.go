package agent

import (
	"sync"
	"time"

	"github.com/ml8s/agentgo/internal/execution"
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

	llmCalls           int
	promptTokens       int
	completionTokens   int
	thoughtTokens      int
	totalTokens        int64
	cachedInputTokens  int64
	modelUsage         []execution.ModelTokenUsage
	modelUsageIndexes  map[string]int
	pendingClientTools []execution.PendingClientTool
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
		modelUsageIndexes: make(map[string]int),
	}
}

// usage returns the aggregated model usage recorded for this run so far.
func (s *runExecution) usage() execution.RunUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return execution.RunUsage{
		LLMCalls:          s.llmCalls,
		PromptTokens:      s.promptTokens,
		CompletionTokens:  s.completionTokens,
		ThoughtTokens:     s.thoughtTokens,
		TotalTokens:       int(s.totalTokens),
		CachedInputTokens: int(s.cachedInputTokens),
		Models:            s.modelUsageSnapshot(),
	}
}

// addModelUsage records usage as provider/model domain facts. The run-level
// counters remain totals; AG-UI must not label every token with the
// entrypoint model.
func (s *runExecution) addModelUsage(provider, model string, usage execution.LLMTokenUsage) {
	if s.modelUsageIndexes == nil {
		s.modelUsageIndexes = make(map[string]int)
	}
	key := provider + "\x00" + model
	index, exists := s.modelUsageIndexes[key]
	if !exists {
		s.modelUsage = append(s.modelUsage, execution.ModelTokenUsage{
			Provider: provider,
			Model:    model,
		})
		index = len(s.modelUsage) - 1
		s.modelUsageIndexes[key] = index
	}

	entry := &s.modelUsage[index]
	entry.LLMCalls++
	s.llmCalls++
	s.promptTokens += int(usage.PromptTokens)
	s.completionTokens += int(usage.CompletionTokens)
	s.thoughtTokens += int(usage.ThoughtTokens)
	s.totalTokens += usage.TotalTokens
	s.cachedInputTokens += usage.CachedInputTokens
	entry.PromptTokens += int(usage.PromptTokens)
	entry.CompletionTokens += int(usage.CompletionTokens)
	entry.ThoughtTokens += int(usage.ThoughtTokens)
	entry.TotalTokens += int(usage.TotalTokens)
	entry.CachedInputTokens += int(usage.CachedInputTokens)
}

func (s *runExecution) modelUsageSnapshot() []execution.ModelTokenUsage {
	if len(s.modelUsage) == 0 {
		return nil
	}
	return append([]execution.ModelTokenUsage(nil), s.modelUsage...)
}

func (s *runExecution) pauseWithClientTools(pending []execution.PendingClientTool) {
	s.pendingClientTools = append([]execution.PendingClientTool(nil), pending...)
}

type llmCallRuntime struct {
	id                string
	model             string
	agentName         string
	instructionDigest string
	startedAt         time.Time
}
