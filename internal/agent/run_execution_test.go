package agent

import (
	"testing"

	"github.com/ml8s/agentgo/internal/execution"
)

func TestRunExecutionUsageGroupsTokensByProviderAndModel(t *testing.T) {
	scope := newRunExecution(runExecutionParams{model: "entrypoint-model"})

	scope.addModelUsage("provider-a", "model-a", execution.LLMTokenUsage{
		PromptTokens:      7,
		CompletionTokens:  3,
		ThoughtTokens:     1,
		TotalTokens:       11,
		CachedInputTokens: 2,
	})
	scope.addModelUsage("provider-b", "model-b", execution.LLMTokenUsage{
		PromptTokens:      5,
		CompletionTokens:  4,
		TotalTokens:       9,
		CachedInputTokens: 1,
	})
	scope.addModelUsage("provider-a", "model-a", execution.LLMTokenUsage{
		PromptTokens: 2,
		TotalTokens:  2,
	})

	usage := scope.usage()
	if usage.LLMCalls != 3 || usage.PromptTokens != 14 || usage.CompletionTokens != 7 ||
		usage.ThoughtTokens != 1 || usage.TotalTokens != 22 || usage.CachedInputTokens != 3 {
		t.Fatalf("run usage = %#v", usage)
	}
	if len(usage.Models) != 2 {
		t.Fatalf("model usage entries = %#v, want 2", usage.Models)
	}

	byKey := make(map[string]execution.ModelTokenUsage, len(usage.Models))
	for _, modelUsage := range usage.Models {
		byKey[modelUsage.Provider+"/"+modelUsage.Model] = modelUsage
	}

	a := byKey["provider-a/model-a"]
	if a.LLMCalls != 2 || a.PromptTokens != 9 || a.CompletionTokens != 3 ||
		a.TotalTokens != 13 || a.CachedInputTokens != 2 {
		t.Fatalf("provider-a/model-a usage = %#v", a)
	}
	b := byKey["provider-b/model-b"]
	if b.LLMCalls != 1 || b.PromptTokens != 5 || b.CompletionTokens != 4 ||
		b.TotalTokens != 9 || b.CachedInputTokens != 1 {
		t.Fatalf("provider-b/model-b usage = %#v", b)
	}
}
