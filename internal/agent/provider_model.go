package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"

	"github.com/ml8s/agentgo/internal/domain"
	"github.com/ml8s/agentgo/internal/execution"
	"github.com/openai/openai-go/v3/option"
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
	ProviderOpenAI           = "openai"
	ProviderAnthropic        = "anthropic"
	ProviderGemini           = "gemini"
	ProviderOpenAICompatible = "openai-compatible"
	ProviderDeepSeek         = "deepseek"
	ProviderZhipu            = "zhipu"
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

func debugOpenAIMiddleware(config Config) option.RequestOption {
	return option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		if os.Getenv("AGENTGO_LOG_LEVEL") != "debug" {
			return next(req)
		}

		var requestBody []byte
		if req.Body != nil {
			requestBody, _ = io.ReadAll(req.Body)
			req.Body = io.NopCloser(bytes.NewReader(requestBody))
		}
		config.Logger.Debug("openai_provider_request",
			"method", req.Method,
			"url", req.URL.String(),
			"body", string(requestBody),
		)

		response, err := next(req)
		if err != nil {
			config.Logger.Debug("openai_provider_response_failed", "error", err.Error())
			return response, err
		}
		responseBody, readErr := io.ReadAll(response.Body)
		if readErr == nil {
			config.Logger.Debug("openai_provider_response",
				"status", response.Status,
				"body", string(responseBody),
			)
			response.Body = io.NopCloser(bytes.NewReader(responseBody))
		}
		return response, err
	})
}

// defaultModelFactory dispatches to the ADK model package matching the
// configured provider. Unrecognized provider names retain the OpenAI
// Responses behavior used by generic OpenAI-compatible gateways; explicitly
// named Chat Completions providers opt into that compatibility protocol.
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

// usesChatCompletions reports whether the named provider declares the Chat
// Completions compatibility contract. Provider selection is explicit; base
// URL sniffing must not change protocol semantics.
func usesChatCompletions(provider string) bool {
	switch provider {
	case ProviderOpenAICompatible, ProviderDeepSeek, ProviderZhipu:
		return true
	default:
		return false
	}
}

// newOpenAIModelFactory builds OpenAI-compatible model clients. json_object
// compatibility is applied by the provider-aware adapter when the operator
// selected json_object mode.
func newOpenAIModelFactory(config Config) ModelBuilderFactory {
	// OpenAI-compatible providers other than "openai" use json_object mode;
	// provider "openai" requests json_schema natively through the raw schema
	// callback. Anthropic and gemini dispatch to their own factories above.
	useJSONObject := execution.DefaultStructuredOutputMode(config.Provider) == execution.StructuredOutputJSONObject
	return func(_ context.Context, modelName string) (model.LLM, error) {
		// ADK defaults to OpenAI Responses. Explicit third-party providers
		// that declare Chat Completions use that stable compatibility API;
		// their Responses support is inconsistent, especially with
		// tool-call continuations.
		api := openaimodel.APIResponses
		if usesChatCompletions(config.Provider) {
			api = openaimodel.APIChatCompletions
		}
		built, err := openaimodel.NewModel(context.Background(), modelName, &openaimodel.ClientConfig{
			APIKey:     config.ModelAPIKey,
			BaseURL:    config.ModelBaseURL,
			HTTPClient: outboundModelClient(config),
			Options: []option.RequestOption{
				debugOpenAIMiddleware(config),
			},
			API: api,
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
