package agent

import (
	"context"
	"testing"

	"github.com/ml8s/agentgo/internal/execution"
	"google.golang.org/adk/v2/model"
)

func TestModelFactoryDispatchesByProvider(t *testing.T) {
	t.Parallel()
	providers := []string{ProviderOpenAI, "zhipu", "deepseek", ""}
	for _, provider := range providers {
		config := Config{Provider: provider}
		factory := newModelFactory(config)
		if factory == nil {
			t.Fatalf("provider %q: factory is nil", provider)
		}
		// Construction against a real endpoint fails on missing credentials;
		// the factory must be callable without panicking.
		_, _ = factory(context.Background(), "test-model")
	}
}

func TestOpenAIProtocolSelectionIsExplicit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		want     bool
	}{
		{ProviderOpenAI, false},
		{ProviderOpenAICompatible, true},
		{ProviderDeepSeek, true},
		{ProviderZhipu, true},
		{"test-provider", false},
		{"", false},
	}
	for _, testCase := range tests {
		if got := usesChatCompletions(testCase.provider); got != testCase.want {
			t.Fatalf("usesChatCompletions(%q) = %v, want %v", testCase.provider, got, testCase.want)
		}
	}
}

func TestModelFactoryUsesOverride(t *testing.T) {
	t.Parallel()
	override := &fakeLLM{}
	config := Config{Provider: ProviderAnthropic, modelOverride: override}
	factory := newModelFactory(config)
	built, err := factory(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if built != override {
		t.Fatalf("override factory returned %#v, want %p", built, override)
	}
}

func TestModelFactoryUsesExplicitBuilder(t *testing.T) {
	t.Parallel()
	override := &fakeLLM{}
	config := Config{
		Provider:     ProviderGemini,
		ModelBuilder: func(_ context.Context, _ string) (model.LLM, error) { return override, nil },
	}
	factory := newModelFactory(config)
	built, err := factory(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if built != override {
		t.Fatalf("explicit builder returned %#v, want %p", built, override)
	}
}

func TestDefaultStructuredOutputMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		want     string
	}{
		{ProviderOpenAI, execution.StructuredOutputJSONSchema},
		{ProviderAnthropic, execution.StructuredOutputNative},
		{ProviderGemini, execution.StructuredOutputNative},
		{"zhipu", execution.StructuredOutputJSONObject},
		{"", execution.StructuredOutputJSONObject},
	}
	for _, tc := range tests {
		got := execution.DefaultStructuredOutputMode(tc.provider)
		if got != tc.want {
			t.Fatalf("DefaultStructuredOutputMode(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}
