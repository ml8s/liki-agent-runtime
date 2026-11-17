package agent

import (
	"context"
	"net/http"

	"github.com/ml8s/agentgo/internal/domain"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/anthropicmodel"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

// Provider names select the ADK model implementation used for LLM calls.
// The value is also the audit provenance label and the structured-output
// mode hint. Unrecognized values fall through to the OpenAI-compatible
// adapter, which remains the de facto cross-provider endpoint standard.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderGemini    = "gemini"
)

// ModelBuilderFactory constructs one ADK model.LLM for a named model on the
// configured provider. The runtime owns the port; the default implementation
// dispatches to ADK's official model packages. Callers may inject a custom
// factory through Config.ModelBuilder.
type ModelBuilderFactory func(ctx context.Context, modelName string) (model.LLM, error)

// newModelFactory returns the effective model factory after resolving the
// test override, explicit injection, or built-in provider dispatch.
func newModelFactory(config Config) ModelBuilderFactory {
	if config.modelOverride != nil {
		return func(_ context.Context, _ string) (model.LLM, error) {
			return config.modelOverride, nil
		}
	}
	if config.ModelBuilder != nil {
		return config.ModelBuilder
	}
	return defaultModelFactory(config)
}

// defaultModelFactory dispatches to the ADK model package matching the
// configured provider. OpenAI-compatible is the fallback for both "openai"
// and unrecognized provider values.
func defaultModelFactory(config Config) ModelBuilderFactory {
	switch config.Provider {
	case ProviderAnthropic:
		return newAnthropicModelFactory(config)
	case ProviderGemini:
		return newGeminiModelFactory(config)
	default:
		return newOpenAIModelFactory(config)
	}
}

// outboundModelClient returns the shared instrumented HTTP client for
// outbound LLM traffic.
func outboundModelClient(config Config) *http.Client {
	return &http.Client{
		Timeout:   config.ModelTimeout,
		Transport: otelhttp.NewTransport(config.outboundTransport),
	}
}

// newOpenAIModelFactory builds OpenAI-compatible model clients. json_object
// compatibility is applied by the provider-aware adapter when the operator
// selected json_object mode.
func newOpenAIModelFactory(config Config) ModelBuilderFactory {
	// OpenAI-compatible providers other than "openai" use json_object mode.
	// Provider "openai" requests json_schema natively through the raw schema
	// callback; anthropic and gemini dispatch to their own factories above.
	useJSONObject := config.Provider != ProviderOpenAI
	return func(_ context.Context, modelName string) (model.LLM, error) {
		built, err := openaimodel.NewModel(context.Background(), modelName, &openaimodel.ClientConfig{
			APIKey:     config.ModelAPIKey,
			BaseURL:    config.ModelBaseURL,
			HTTPClient: outboundModelClient(config),
		})
		if err != nil {
			return nil, domain.NewError(domain.CodeLLMUnavailable, "create OpenAI-compatible model", err)
		}
		if !useJSONObject {
			return built, nil
		}
		compatModel, err := newJSONObjectModel(built)
		if err != nil {
			return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create provider structured output model", err)
		}
		return compatModel, nil
	}
}

// newAnthropicModelFactory builds native Anthropic Claude model clients.
// ADK's anthropicmodel handles genai.Schema natively; no structured-output
// shim is required.
func newAnthropicModelFactory(config Config) ModelBuilderFactory {
	return func(ctx context.Context, modelName string) (model.LLM, error) {
		built, err := anthropicmodel.NewModel(ctx, modelName, anthropicmodel.ClientConfig{
			APIKey:     config.ModelAPIKey,
			BaseURL:    config.ModelBaseURL,
			HTTPClient: outboundModelClient(config),
		})
		if err != nil {
			return nil, domain.NewError(domain.CodeLLMUnavailable, "create Anthropic model", err)
		}
		return built, nil
	}
}

// newGeminiModelFactory builds native Google Gemini model clients.
// ADK's gemini model handles genai.Schema natively; no structured-output
// shim is required.
func newGeminiModelFactory(config Config) ModelBuilderFactory {
	return func(ctx context.Context, modelName string) (model.LLM, error) {
		built, err := gemini.NewModel(ctx, modelName, &genai.ClientConfig{
			APIKey:  config.ModelAPIKey,
			Backend: genai.BackendGeminiAPI,
			HTTPOptions: genai.HTTPOptions{
				BaseURL: config.ModelBaseURL,
			},
			HTTPClient: outboundModelClient(config),
		})
		if err != nil {
			return nil, domain.NewError(domain.CodeLLMUnavailable, "create Gemini model", err)
		}
		return built, nil
	}
}
