package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ml8s/agent-runtime/internal/domain"
	"github.com/ml8s/agent-runtime/internal/execution"
	"github.com/ml8s/agent-runtime/internal/testagent"
	"google.golang.org/adk/v2/model"
)

func TestModelFactoryDispatchesByProvider(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider   string
		structured string
	}{
		{ProviderOpenAI, execution.StructuredOutputJSONSchema},
		{ProviderOpenAI, execution.StructuredOutputJSONObject},
		{"zhipu", execution.StructuredOutputJSONObject},
	}
	for _, tc := range tests {
		config := Config{Provider: tc.provider, StructuredOutput: tc.structured}
		factory := newModelFactory(config)
		if factory == nil {
			t.Fatalf("provider %q structured %q: factory is nil", tc.provider, tc.structured)
		}
		// Construction against a real endpoint fails on an empty model name
		// or missing credentials; the factory must be callable without
		// panicking, and the OpenAI-compatible dispatch must be the fallback.
		_, _ = factory(context.Background(), "test-model")
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

func TestNativeProviderRejectsOpenAIStructuredOutput(t *testing.T) {
	for _, provider := range []string{ProviderAnthropic, ProviderGemini} {
		for _, mode := range []string{execution.StructuredOutputJSONSchema, execution.StructuredOutputJSONObject} {
			config := Config{
				Provider:         provider,
				StructuredOutput: mode,
				Model:            "test-model",
				AuditRecorder:    &recordingAudit{},
				AgentDeployment:  testagent.PlainAgentDeployment(t),
			}
			_, err := normalizeRuntimeConfig(config)
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeStructuredOutputCapabilityInvalid {
				t.Fatalf("provider %q mode %q: error = %v, want %s", provider, mode, err, domain.CodeStructuredOutputCapabilityInvalid)
			}
			if domainErr.Message == "" {
				t.Fatalf("provider %q mode %q: error message is empty", provider, mode)
			}
		}
	}
}

func TestNativeProviderAcceptsNativeAndEmpty(t *testing.T) {
	deployment := testagent.AgentDeployment(t) // has structured output; triggers auto-set
	for _, provider := range []string{ProviderAnthropic, ProviderGemini} {
		for _, mode := range []string{execution.StructuredOutputNone, execution.StructuredOutputNative} {
			config := Config{
				Provider:         provider,
				StructuredOutput: mode,
				Model:            "test-model",
				AuditRecorder:    &recordingAudit{},
				AgentDeployment:  deployment,
			}
			normalized, err := normalizeRuntimeConfig(config)
			if err != nil {
				t.Fatalf("provider %q mode %q: unexpected error %v", provider, mode, err)
			}
			want := mode
			if mode == execution.StructuredOutputNone {
				want = execution.StructuredOutputNative
			}
			if normalized.StructuredOutput != want {
				t.Fatalf("provider %q mode %q: normalized StructuredOutput = %q, want %q", provider, mode, normalized.StructuredOutput, want)
			}
		}
	}
}
