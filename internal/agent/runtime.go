// Package agent owns the single ADK execution graph and its external model and
// MCP dependencies. Protocol packages adapt this runtime; they never create a
// second execution path.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/ml8s/agent-runtime/internal/audit"
	"github.com/ml8s/agent-runtime/internal/domain"
	platformconfig "github.com/ml8s/agent-runtime/internal/platform/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/plugin/retryandreflect"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
	"google.golang.org/genai"
)

const (
	// maxCompletedRunIDs bounds duplicate-run detection without turning the
	// in-memory lifecycle registry into another durable run store.
	maxCompletedRunIDs = 4096
)

// Runtime owns the single ADK execution graph, its model and MCP
// dependencies, and the durable audit hooks that protocol adapters drive.
type Runtime struct {
	config            Config
	definition        *agentdeployment.AgentDeployment
	entrypoint        *agentdeployment.ResolvedAgentDefinition
	root              agent.Agent
	sessions          session.Service
	ephemeralSessions session.Service
	runner            *runner.Runner
	ephemeralRunner   *runner.Runner
	runnerConfig      runner.Config
	callAuditor       *llmCallAuditor
	delegationAuditor *DelegationAuditor
	toolAuditor       *ToolExecutionAuditor
	tracer            trace.Tracer
	projectorIndex    execution.EventProjectorIndex
	runs              *runLifecycle
	runSlots          chan struct{}
}

// NewRuntime builds the runtime from a validated deployment and its
// dependencies. It fails closed on any invalid or missing dependency.
// normalizeRuntimeConfig fills zero-value defaults, validates required and
// consistency constraints, and returns a build-ready copy. Zero-value
// fallbacks must match platform/config Load defaults.
func normalizeRuntimeConfig(config Config) (Config, error) {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.AppName == "" {
		config.AppName = "agent-runtime"
	}
	if config.Model == "" {
		return Config{}, domain.NewError(domain.CodeLLMModelMissing, "LLM model is required", domain.ErrInvalidInput)
	}
	if config.ModelTimeout <= 0 {
		config.ModelTimeout = platformconfig.DefaultLLMTimeout
	}
	if config.AuditRecorder == nil {
		return Config{}, domain.NewError(domain.CodeAuditRecorderMissing, "audit recorder is required", domain.ErrInvalidInput)
	}
	if config.AgentDeployment == nil {
		return Config{}, domain.NewError(domain.CodeAgentDefinitionMissing, "AgentDefinition is required", domain.ErrInvalidInput)
	}
	switch config.StructuredOutput {
	case execution.StructuredOutputNone, execution.StructuredOutputJSONSchema, execution.StructuredOutputJSONObject:
	default:
		return Config{}, domain.NewError(domain.CodeStructuredOutputCapabilityInvalid, "structured output capability is invalid", domain.ErrInvalidInput)
	}
	structuredAgents := 0
	for _, candidate := range config.AgentDeployment.Spec.Agents {
		if candidate.Output.Structured() {
			structuredAgents++
		}
	}
	switch {
	case structuredAgents == 0 && config.StructuredOutput != execution.StructuredOutputNone:
		return Config{}, domain.NewError(domain.CodeStructuredOutputCapabilityInvalid, "structured output mode is set but no Agent declares an output schema", domain.ErrInvalidInput)
	case structuredAgents > 0 && config.StructuredOutput == execution.StructuredOutputNone:
		config.StructuredOutput = execution.DefaultStructuredOutputMode(config.Provider)
	}
	if config.MCPTimeout <= 0 {
		config.MCPTimeout = platformconfig.DefaultMCPTimeout
	}
	if config.MCPHealthTimeout <= 0 {
		// Zero-value fallback: dependency discovery follows the tool-call
		// budget unless the deployment separates the two.
		config.MCPHealthTimeout = config.MCPTimeout
	}
	if config.MaxConcurrentRuns <= 0 {
		config.MaxConcurrentRuns = platformconfig.DefaultMaxConcurrentRuns
	}
	if config.MaxConcurrentRuns > platformconfig.MaxConcurrentRunsLimit {
		return Config{}, domain.NewError(
			domain.CodeRuntimeInitFailed,
			fmt.Sprintf("max concurrent runs must not exceed %d", platformconfig.MaxConcurrentRunsLimit),
			nil,
		)
	}
	config.outboundTransport = newOutboundTransport(config.MaxConcurrentRuns)
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.TracerProvider == nil {
		config.TracerProvider = otel.GetTracerProvider()
	}
	return config, nil
}

// recoverInterruptedAudits closes audit lifecycles left open by a process
// interruption with synthetic runtime_interrupted terminal failures
// (append-only), before the runtime accepts traffic.
func recoverInterruptedAudits(config Config) error {
	if recoverer, ok := config.AuditRecorder.(audit.InterruptionRecoverer); ok {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := recoverer.RecoverInterrupted(recoveryCtx, config.Now()); err != nil {
			return domain.NewError(domain.CodeRuntimeInitFailed, "recover interrupted audit events", err)
		}
	}
	return nil
}

// newModelBuilder builds one OpenAI-compatible model client. json_object
// compatibility is applied by the provider-aware adapter.
func newModelBuilder(config Config) func(string) (model.LLM, error) {
	if config.modelOverride != nil {
		return func(string) (model.LLM, error) { return config.modelOverride, nil }
	}
	useJSONObject := config.StructuredOutput == execution.StructuredOutputJSONObject
	build := func(modelName string) (model.LLM, error) {
		built, err := openaimodel.NewModel(context.Background(), modelName, &openaimodel.ClientConfig{
			APIKey:  config.ModelAPIKey,
			BaseURL: config.ModelBaseURL,
			HTTPClient: &http.Client{
				Timeout:   config.ModelTimeout,
				Transport: otelhttp.NewTransport(config.outboundTransport),
			},
		})
		if err != nil {
			return nil, domain.NewError(domain.CodeLLMUnavailable, "create LLM model", err)
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
	return build
}

// NewRuntime builds the runtime from a validated deployment and its
// dependencies. It fails closed on any invalid or missing dependency.
func NewRuntime(config Config) (*Runtime, error) {
	config, err := normalizeRuntimeConfig(config)
	if err != nil {
		return nil, err
	}
	if err := recoverInterruptedAudits(config); err != nil {
		return nil, err
	}
	tracer := config.TracerProvider.Tracer("github.com/ml8s/agent-runtime/agent")
	// Protocol clients own durable conversation/product history. Each ADK
	// session is deliberately run-scoped working state and must not become a
	// second durable conversation store.
	durableSessions := config.SessionService
	if durableSessions == nil {
		durableSessions = session.InMemoryService()
	}
	// Run-scoped working sessions (AG-UI) live in-process: they are seeded
	// from caller-owned history, deleted at terminal execution, and never
	// outlive the process. Durable protocol sessions (A2A ContextID) stay on
	// the injected service. Splitting the runners by protocol surface, rather
	// than routing on session-ID prefixes, removes any heuristic that a
	// client-controlled A2A ContextID could abuse.
	ephemeralSessions := config.ephemeralSessionServiceOverride
	if ephemeralSessions == nil {
		ephemeralSessions = session.InMemoryService()
	}
	entrypoint, err := config.AgentDeployment.EntrypointDefinition()
	if err != nil {
		return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, "select entrypoint AgentDefinition", err)
	}
	models, err := newModelRegistry(config, config.AgentDeployment)
	if err != nil {
		return nil, err
	}
	temperature := float32(config.Temperature)
	callAuditor := newLLMCallAuditor(config.AuditRecorder, config.Metrics, config.Provider, config.AgentDeployment, config.Now)
	mcpToolsets, err := newMCPToolsets(config)
	if err != nil {
		return nil, err
	}
	warmMCPCatalogs(config, mcpToolsets)
	toolAuditor := newToolExecutionAuditor(
		config.AuditRecorder,
		config.Metrics,
		config.Now,
		config.Provider,
		config.AgentDeployment,
		callAuditor.auditScope,
	)
	delegationAuditor := newDelegationAuditor(
		config.AuditRecorder,
		config.Metrics,
		config.Now,
		config.AgentDeployment,
		entrypoint.Name,
		callAuditor.auditScope,
	)
	graph := newAgentGraphBuilder(config.AgentDeployment, entrypoint.Name, agentGraphDeps{
		mcpToolsets:       mcpToolsets,
		modelName:         config.Model,
		resolveModel:      models.resolve,
		temperature:       temperature,
		maxOutputTokens:   config.MaxOutputTokens,
		callAuditor:       callAuditor,
		delegationAuditor: delegationAuditor,
		toolAuditor:       toolAuditor,
	})
	root, err := graph.build(entrypoint)
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create ADK agent", err)
	}
	projectorIndex := execution.NewEventProjectorIndex(config.AgentDeployment)
	retryPlugin, err := retryandreflect.New(
		retryandreflect.WithMaxRetries(2),
		retryandreflect.WithErrorIfRetryExceeded(true),
	)
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create tool retry plugin", err)
	}
	attemptPlugin, err := plugin.New(plugin.Config{
		Name: "agent.runtime.audit_attempt",
		BeforeRunCallback: func(invocation agent.InvocationContext) (*genai.Content, error) {
			if attemptID, ok := invocation.Value(auditAttemptKey{}).(string); ok && attemptID != "" {
				callAuditor.bindInvocation(invocation.Session().ID(), invocation.InvocationID(), attemptID)
			}
			return nil, nil
		},
		AfterRunCallback: func(invocation agent.InvocationContext) {
			if attemptID, ok := invocation.Value(auditAttemptKey{}).(string); ok && attemptID != "" {
				callAuditor.unbindInvocations(attemptID)
			}
		},
	})
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create audit attempt plugin", err)
	}
	runnerConfig := runner.Config{
		AppName:           config.AppName,
		Agent:             root,
		SessionService:    durableSessions,
		AutoCreateSession: true,
		PluginConfig: runner.PluginConfig{
			Plugins:      []*plugin.Plugin{retryPlugin, attemptPlugin},
			CloseTimeout: 5 * time.Second,
		},
	}
	runnerInstance, err := runner.New(runnerConfig)
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create ADK runner", err)
	}
	ephemeralRunnerConfig := runnerConfig
	ephemeralRunnerConfig.SessionService = ephemeralSessions
	ephemeralRunnerInstance, err := runner.New(ephemeralRunnerConfig)
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create ADK ephemeral runner", err)
	}
	return &Runtime{
		config:            config,
		root:              root,
		sessions:          durableSessions,
		ephemeralSessions: ephemeralSessions,
		runner:            runnerInstance,
		ephemeralRunner:   ephemeralRunnerInstance,
		runnerConfig:      runnerConfig,
		callAuditor:       callAuditor,
		delegationAuditor: delegationAuditor,
		toolAuditor:       toolAuditor,
		tracer:            tracer,
		projectorIndex:    projectorIndex,
		definition:        config.AgentDeployment,
		entrypoint:        entrypoint,
		runs:              newRunLifecycle(),
		runSlots:          make(chan struct{}, config.MaxConcurrentRuns),
	}, nil
}

func newMCPToolsets(config Config) ([]tool.Toolset, error) {
	toolsets := make([]tool.Toolset, 0, len(config.AgentDeployment.Spec.MCPServers))
	for _, definition := range config.AgentDeployment.Spec.MCPServers {
		resolved, err := resolveMCPServer(definition)
		if err != nil {
			return nil, err
		}
		if _, overridden := config.mcpTransportOverrides[definition.Name]; !overridden {
			if err := validateMCPEndpoint(resolved); err != nil {
				return nil, err
			}
		}
		var transport mcp.Transport = newMCPTransport(config, resolved)
		if override, ok := config.mcpTransportOverrides[definition.Name]; ok {
			transport = override
		}
		toolset, err := mcptoolset.New(mcptoolset.Config{
			Transport: transport,
		})
		if err != nil {
			return nil, domain.NewError(domain.CodeMCPToolsUnavailable, fmt.Sprintf("create MCP toolset %q", definition.Name), err)
		}
		// Memoize standard discovery per logical server; per-Agent allowlist
		// filtering still runs downstream inside tool.FilterToolset.
		toolsets = append(toolsets, newCachedToolset(definition.Name, toolset, mcpCatalogTTL, config.Now, config.Metrics))
	}
	return toolsets, nil
}

type resolvedMCPServer struct {
	definition agentdeployment.MCPServerDefinition
	Endpoint   string
}

func resolveMCPServer(definition agentdeployment.MCPServerDefinition) (resolvedMCPServer, error) {
	endpoint, ok := os.LookupEnv(definition.EndpointEnv)
	if !ok || strings.TrimSpace(endpoint) == "" {
		return resolvedMCPServer{}, domain.NewError(
			domain.CodeMCPEndpointEnvMissing,
			fmt.Sprintf("MCP endpoint environment %q is required for server %q", definition.EndpointEnv, definition.Name),
			nil,
		)
	}
	resolved := resolvedMCPServer{definition: definition, Endpoint: strings.TrimSpace(endpoint)}
	return resolved, nil
}

func validateMCPEndpoint(server resolvedMCPServer) error {
	endpointURL, err := url.Parse(server.Endpoint)
	if err != nil ||
		endpointURL.Scheme != "http" && endpointURL.Scheme != "https" ||
		endpointURL.Host == "" ||
		endpointURL.User != nil {
		return domain.NewError(
			domain.CodeMCPEndpointInvalid,
			fmt.Sprintf(
				"MCP endpoint environment %q must be an absolute HTTP(S) URL without credentials",
				server.definition.EndpointEnv,
			),
			nil,
		)
	}
	return nil
}

// RootAgent exposes the single ADK graph to standard protocol adapters. ADK
// types do not cross further inward than this runtime component.
func (r *Runtime) RootAgent() agent.Agent {
	return r.root
}

// RunnerConfig returns the shared ADK runtime configuration used by protocol
// bindings. The same plugins and run-scoped session service are preserved.
func (r *Runtime) RunnerConfig() runner.Config {
	return r.runnerConfig
}
