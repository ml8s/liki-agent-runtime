// Package agent owns the single ADK execution graph and its external model and
// MCP dependencies. Protocol packages adapt this runtime; they never create a
// second execution path.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/plugin/retryandreflect"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
)

const (
	// structuredOutputStateKeyPrefix matches llmagent.Config.OutputKey. ADK parses
	// the model reply against OutputSchema, clears Event.Output before
	// yielding it, and persists the parsed value into session state under
	// this key. Session state is the framework mechanism for consuming it.
	structuredOutputStateKeyPrefix = "structured_analysis:"

	// maxCompletedRunIDs bounds duplicate-run detection without turning the
	// in-memory lifecycle registry into another durable run store.
	maxCompletedRunIDs = 4096
)

// Runtime owns the single ADK execution graph, its model and MCP
// dependencies, and the durable audit hooks that protocol adapters drive.
type Runtime struct {
	config            Config
	definition        *Deployment
	entrypoint        *AgentDefinition
	root              agent.Agent
	sessions          session.Service
	runner            *runner.Runner
	runnerConfig      runner.Config
	llm               *llmLedger
	delegationAuditor *AgentReferenceAuditor
	toolAuditor       *ToolExecutionAuditor
	tracer            trace.Tracer
	runs              *runLifecycle
	runSlots          chan struct{}
}

type runLifecycle struct {
	mu        sync.Mutex
	active    map[string]struct{}
	completed map[string]struct{}
	order     []string
}

func (r *Runtime) acquireRun(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, runtimeError(err)
	}
	select {
	case r.runSlots <- struct{}{}:
		return func() { <-r.runSlots }, nil
	default:
		return nil, domain.NewError(domain.CodeRuntimeBusy, "runtime is at its concurrent run limit", ctx.Err())
	}
}

func newRunLifecycle() *runLifecycle {
	return &runLifecycle{
		active:    make(map[string]struct{}),
		completed: make(map[string]struct{}),
	}
}

func (l *runLifecycle) begin(runID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.active[runID]; exists {
		return false
	}
	if _, exists := l.completed[runID]; exists {
		return false
	}
	l.active[runID] = struct{}{}
	return true
}

func (l *runLifecycle) finish(runID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.active, runID)
	l.completed[runID] = struct{}{}
	l.order = append(l.order, runID)
	if len(l.order) > maxCompletedRunIDs {
		delete(l.completed, l.order[0])
		l.order = l.order[1:]
	}
}

// NewRuntime builds the runtime from a validated deployment and its
// dependencies. It fails closed on any invalid or missing dependency.
func NewRuntime(config Config) (*Runtime, error) {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.AppName == "" {
		config.AppName = "liki-agent-runtime"
	}
	if config.Model == "" {
		return nil, domain.NewError(domain.CodeLLMModelMissing, "LLM model is required", domain.ErrInvalidInput)
	}
	if config.ModelTimeout <= 0 {
		// Zero-value fallback must match platform/config's Load default.
		config.ModelTimeout = 120 * time.Second
	}
	if config.AuditRecorder == nil {
		return nil, domain.NewError(domain.CodeAuditRecorderMissing, "audit recorder is required", domain.ErrInvalidInput)
	}
	if config.Deployment == nil {
		return nil, domain.NewError(domain.CodeAgentDefinitionMissing, "AgentDefinition is required", domain.ErrInvalidInput)
	}
	entrypoint, err := config.Deployment.EntrypointDefinition()
	if err != nil {
		return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, "select entrypoint AgentDefinition", err)
	}
	switch config.StructuredOutput {
	case StructuredOutputNone, StructuredOutputJSONSchema, StructuredOutputJSONObject:
	default:
		return nil, domain.NewError(domain.CodeStructuredOutputCapabilityInvalid, "structured output capability is invalid", domain.ErrInvalidInput)
	}
	structuredAgents := 0
	for _, candidate := range config.Deployment.Spec.Agents {
		if candidate.Output.Structured() {
			structuredAgents++
		}
	}
	switch {
	case structuredAgents == 0 && config.StructuredOutput != StructuredOutputNone:
		return nil, domain.NewError(domain.CodeStructuredOutputCapabilityInvalid, "structured output mode is set but no Agent declares an output schema", domain.ErrInvalidInput)
	case structuredAgents > 0 && config.StructuredOutput == StructuredOutputNone:
		config.StructuredOutput = DefaultStructuredOutputMode(config.Provider)
	}
	if config.MCPTimeout <= 0 {
		// Zero-value fallback must match platform/config's Load default.
		config.MCPTimeout = 30 * time.Second
	}
	if config.MCPHealthTimeout <= 0 {
		// Zero-value fallback: dependency discovery follows the tool-call
		// budget unless the deployment separates the two.
		config.MCPHealthTimeout = config.MCPTimeout
	}
	if config.MaxConcurrentRuns <= 0 {
		// Zero-value fallback must match platform/config's Load default.
		config.MaxConcurrentRuns = 32
	}
	config.outboundTransport = newOutboundTransport(config.MaxConcurrentRuns)
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if recoverer, ok := config.AuditRecorder.(audit.InterruptionRecoverer); ok {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := recoverer.RecoverInterrupted(recoveryCtx, config.Now()); err != nil {
			return nil, domain.NewError(domain.CodeRuntimeInitFailed, "recover interrupted audit events", err)
		}
	}
	if config.TracerProvider == nil {
		config.TracerProvider = otel.GetTracerProvider()
	}
	tracer := config.TracerProvider.Tracer("github.com/ml8s/liki-agent-runtime/agent")
	// Protocol callers own durable conversation/product history. Each ADK
	// session is deliberately run-scoped working state and must not become a
	// second durable conversation store.
	sessionService := session.InMemoryService()
	if config.SessionService != nil {
		sessionService = config.SessionService
	}

	var resolveModel func(string) (model.LLM, error)
	if config.modelOverride != nil {
		resolveModel = func(string) (model.LLM, error) { return config.modelOverride, nil }
	} else {
		// Use ADK's official OpenAI-compatible model. The provider-aware
		// structured-output adapter handles providers without JSON schema mode.
		useJSONObject := config.StructuredOutput == StructuredOutputJSONObject
		resolveModel = newModelResolver(func(modelName string) (model.LLM, error) {
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
		}).resolve
	}
	temperature := float32(config.Temperature)
	ledger := newLLMLedger(config.AuditRecorder, config.Metrics, config.Provider, config.Deployment, config.Now)
	mcpToolsets, err := newMCPToolsets(config)
	if err != nil {
		return nil, err
	}
	toolAuditor := newToolExecutionAuditor(
		config.AuditRecorder,
		config.Metrics,
		config.Now,
		config.Provider,
		config.Deployment,
		ledger.scope,
	)
	delegationAuditor := newAgentReferenceAuditor(
		config.AuditRecorder,
		config.Metrics,
		config.Now,
		config.Deployment,
		entrypoint.Name,
		ledger.scope,
	)
	builtAgents := make(map[string]agent.Agent, len(config.Deployment.Spec.Agents))
	building := make(map[string]struct{})
	var buildAgent func(*AgentDefinition) (agent.Agent, error)
	buildAgent = func(definition *AgentDefinition) (agent.Agent, error) {
		if built, ok := builtAgents[definition.Name]; ok {
			return built, nil
		}
		if _, active := building[definition.Name]; active {
			return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("agent delegation cycle through %q", definition.Name), nil)
		}
		building[definition.Name] = struct{}{}
		outputKey := ""
		if definition.Output.Structured() {
			outputKey = StructuredOutputStateKey(definition.Name)
		}
		subAgents := make([]agent.Agent, 0, len(definition.SubAgents))
		for _, reference := range definition.SubAgents {
			target, ok := config.Deployment.Agent(reference.Name)
			if !ok {
				return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("sub-agent %q is not defined", reference.Name), nil)
			}
			subAgent, err := buildAgent(target)
			if err != nil {
				return nil, err
			}
			subAgents = append(subAgents, subAgent)
		}
		toolsets := make([]tool.Toolset, 0, len(config.Deployment.Spec.MCPServers))
		for index, server := range config.Deployment.Spec.MCPServers {
			allowed := definition.Tools.Allow[server.Name]
			if len(allowed) == 0 {
				continue
			}
			toolsets = append(toolsets, tool.FilterToolset(
				mcpToolsets[index],
				tool.AllowedToolsPredicate(allowed),
			))
		}
		if binding := skillBinding(definition); binding != nil {
			skillToolset, err := newSkillToolset(binding)
			if err != nil {
				return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("build skill toolset for agent %q: %v", definition.Name, err), nil)
			}
			toolsets = append(toolsets, skillToolset)
		}
		delete(building, definition.Name)
		// Per-Agent model settings fall back to the runtime-wide defaults.
		aiModel, err := resolveModel(definition.EffectiveModel(config.Model))
		if err != nil {
			return nil, err
		}
		built, err := llmagent.New(definition.ADKConfig(ADKAgentRuntime{
			RawOutputSchema: definition.RawOutputSchema,
			Model:           aiModel,
			Toolsets:        toolsets,
			SubAgents:       subAgents,
			Temperature:     definition.EffectiveTemperature(temperature),
			MaxOutputTokens: definition.EffectiveMaxOutputTokens(config.MaxOutputTokens),
			OutputKey:       outputKey,
			IsEntrypoint:    definition.Name == entrypoint.Name,
			BeforeModelCallbacks: []llmagent.BeforeModelCallback{
				ledger.beforeModel,
			},
			AfterModelCallbacks: []llmagent.AfterModelCallback{
				ledger.afterModel,
			},
			OnModelErrorCallbacks: []llmagent.OnModelErrorCallback{
				ledger.onModelError,
			},
			BeforeAgentCallbacks: []agent.BeforeAgentCallback{
				delegationAuditor.BeforeAgent,
			},
			AfterAgentCallbacks: []agent.AfterAgentCallback{
				delegationAuditor.AfterAgent,
			},
			BeforeToolCallbacks: []llmagent.BeforeToolCallback{
				toolAuditor.BeforeTool,
			},
			AfterToolCallbacks: []llmagent.AfterToolCallback{
				toolAuditor.AfterTool,
			},
		}))
		if err != nil {
			return nil, domain.NewError(domain.CodeRuntimeInitFailed, fmt.Sprintf("create ADK agent %q", definition.Name), err)
		}
		builtAgents[definition.Name] = built
		return built, nil
	}
	retryPlugin, err := retryandreflect.New(
		retryandreflect.WithMaxRetries(2),
		retryandreflect.WithErrorIfRetryExceeded(true),
	)
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create tool retry plugin", err)
	}
	plugins := []*plugin.Plugin{retryPlugin}
	root, err := buildAgent(entrypoint)
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create ADK agent", err)
	}
	runnerConfig := runner.Config{
		AppName:           config.AppName,
		Agent:             root,
		SessionService:    sessionService,
		AutoCreateSession: true,
		PluginConfig: runner.PluginConfig{
			Plugins:      plugins,
			CloseTimeout: 5 * time.Second,
		},
	}
	runnerInstance, err := runner.New(runnerConfig)
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, "create ADK runner", err)
	}
	return &Runtime{
		config:            config,
		root:              root,
		sessions:          sessionService,
		runner:            runnerInstance,
		runnerConfig:      runnerConfig,
		llm:               ledger,
		delegationAuditor: delegationAuditor,
		toolAuditor:       toolAuditor,
		tracer:            tracer,
		definition:        config.Deployment,
		entrypoint:        entrypoint,
		runs:              newRunLifecycle(),
		runSlots:          make(chan struct{}, config.MaxConcurrentRuns),
	}, nil
}

func newMCPToolsets(config Config) ([]tool.Toolset, error) {
	toolsets := make([]tool.Toolset, 0, len(config.Deployment.Spec.MCPServers))
	for _, definition := range config.Deployment.Spec.MCPServers {
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
		toolsets = append(toolsets, toolset)
	}
	return toolsets, nil
}

type resolvedMCPServer struct {
	definition MCPServerDefinition
	Endpoint   string
	Token      string
}

func resolveMCPServer(definition MCPServerDefinition) (resolvedMCPServer, error) {
	endpoint, ok := os.LookupEnv(definition.EndpointEnv)
	if !ok || strings.TrimSpace(endpoint) == "" {
		return resolvedMCPServer{}, domain.NewError(
			domain.CodeMCPEndpointEnvMissing,
			fmt.Sprintf("MCP endpoint environment %q is required for server %q", definition.EndpointEnv, definition.Name),
			nil,
		)
	}
	resolved := resolvedMCPServer{definition: definition, Endpoint: strings.TrimSpace(endpoint)}
	if definition.TokenEnv == "" {
		return resolved, nil
	}
	token, ok := os.LookupEnv(definition.TokenEnv)
	if !ok {
		return resolvedMCPServer{}, domain.NewError(
			domain.CodeMCPEndpointEnvMissing,
			fmt.Sprintf("MCP token environment %q is required for server %q", definition.TokenEnv, definition.Name),
			nil,
		)
	}
	resolved.Token = token
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

// AuditRunScope identifies an externally driven ADK execution for the LLM
// audit ledger. It does not create a second execution path.
type AuditRunScope struct {
	RunID    string
	ThreadID string
	UserID   string
	Protocol string
}

// BeginAuditRun attaches protocol identity to model callbacks when an official
// protocol executor drives the shared ADK runtime directly.
func (r *Runtime) BeginAuditRun(ctx context.Context, sessionID string, scope AuditRunScope) error {
	scope.RunID = strings.TrimSpace(scope.RunID)
	scope.ThreadID = strings.TrimSpace(scope.ThreadID)
	scope.UserID = strings.TrimSpace(scope.UserID)
	if scope.RunID == "" || scope.ThreadID == "" || scope.UserID == "" {
		return domain.NewError(domain.CodeAuditScopeRequired, "run, thread, and user identifiers are required", domain.ErrInvalidInput)
	}
	if !ValidIdentifier(scope.RunID) {
		return domain.NewError(domain.CodeRunIDInvalid, "run id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	}
	if !ValidIdentifier(scope.ThreadID) {
		return domain.NewError(domain.CodeThreadIDInvalid, "thread id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	}
	if !ValidIdentifier(scope.UserID) {
		return domain.NewError(domain.CodeUserIDInvalid, "user id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	}
	if err := r.ensureDurableRunID(ctx, scope.RunID); err != nil {
		return err
	}
	if sessionID == "" {
		return domain.NewError(domain.CodeAuditSessionRequired, "audit session identifier is required", domain.ErrInvalidInput)
	}
	if !r.runs.begin(scope.RunID) {
		return domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
	}
	releaseRun, err := r.acquireRun(ctx)
	if err != nil {
		r.runs.finish(scope.RunID)
		return err
	}
	ledgerScope := &llmRunScope{
		runID:             domain.ID(scope.RunID),
		threadID:          domain.ID(scope.ThreadID),
		userID:            scope.UserID,
		protocol:          scope.Protocol,
		model:             r.entrypoint.EffectiveModel(r.config.Model),
		instructionDigest: r.entrypoint.InstructionDigest,
		traceID:           traceIDFromContext(ctx),
		spanID:            spanIDFromContext(ctx),
		definitionName:    r.definition.Metadata.Name,
		definitionVersion: r.definition.Metadata.Version,
		definitionDigest:  r.definition.Digest,
		startedAt:         r.config.Now(),
		releaseRun:        releaseRun,
		activeCalls:       make(map[string]llmCallRuntime),
	}
	if !r.llm.begin(sessionID, ledgerScope) {
		releaseRun()
		r.runs.finish(scope.RunID)
		return domain.NewError(domain.CodeAuditSessionActive, "an audit run is already active for this session", domain.ErrInvalidInput)
	}
	event := r.runEvent(ctx, ledgerScope, audit.EventRunStarted, audit.StatusRunning, nil)
	if err := r.recordAuditWithRetry(ctx, &event); err != nil {
		r.runs.finish(scope.RunID)
		_, _ = r.llm.end(sessionID, err)
		_ = r.delegationAuditor.FailPending(ctx, ledgerScope)
		_ = r.toolAuditor.FailPending(ctx, ledgerScope)
		releaseRun()
		return err
	}
	return nil
}

// EndAuditRun completes an externally driven audit lifecycle exactly once.
//
// The run scope is released only after the terminal run event is durably
// appended. A transient append failure therefore returns without releasing the
// scope or the concurrency slot, so the executor's cleanup callback retries the
// terminal append rather than stranding an orphaned started event. Startup
// recovery remains the last resort if the process exits before a retry succeeds.
func (r *Runtime) EndAuditRun(ctx context.Context, sessionID string, runErr error) error {
	ctx = context.WithoutCancel(ctx)
	if sessionID == "" {
		return nil
	}
	scope, existed := r.llm.scope(sessionID)
	if !existed {
		return nil
	}

	// 终态证据事务（与 Run 共用唯一实现，见 run_evidence.go）：关闭
	// in-flight → 补偿 → 终态事件。追加失败时直接返回——作用域保留，
	// 执行方清理回调负责重试；在此之前不释放任何进程状态。因果与补偿
	// 错误已编码为终态事件的失败原因。
	if err := r.closeRunEvidence(ctx, scope, runErr); err != nil {
		return err
	}

	// Terminal evidence is durable: release process state exactly once. Cleanup
	// is operational noise and must not change the durable execution outcome.
	if deleteErr := r.sessions.Delete(ctx, &session.DeleteRequest{
		AppName:   r.config.AppName,
		UserID:    scope.userID,
		SessionID: sessionID,
	}); deleteErr != nil {
		r.config.Logger.WarnContext(ctx, "external_run_session_cleanup_failed",
			"run_id", string(scope.runID),
			"error", runtimeError(deleteErr).Error(),
		)
	}
	r.llm.forget(sessionID)
	if scope.releaseRun != nil {
		scope.releaseRun()
	}
	r.runs.finish(string(scope.runID))
	return nil
}

// runEvent is the single constructor for run-lifecycle audit events, shared by
// the protocol-driven Run path and the externally driven BeginAuditRun /
// EndAuditRun path.
func (r *Runtime) runEvent(
	ctx context.Context,
	scope *llmRunScope,
	eventType audit.EventType,
	status audit.Status,
	cause error,
) audit.Event {
	event := scopedEvent(scope)
	event.ID = string(scope.runID) + ":" + string(eventType)
	event.Type = eventType
	event.OccurredAt = r.config.Now()
	event.AgentName = r.entrypoint.Name
	event.AgentVersion = r.entrypoint.Version
	event.AgentDefinitionDigest = r.entrypoint.Digest
	event.Model = r.entrypoint.EffectiveModel(r.config.Model)
	event.Provider = r.config.Provider
	event.Status = status
	event.DurationMS = r.config.Now().Sub(scope.startedAt).Milliseconds()
	event.TraceID = traceIDFromContext(ctx)
	event.SpanID = spanIDFromContext(ctx)
	if cause != nil {
		event.ErrorCode, event.ErrorMessage = errorFields(cause)
	}
	return event
}

// errorFields maps an arbitrary error to stable audit error fields.
func errorFields(cause error) (code, message string) {
	runtimeErr := runtimeError(cause)
	var domainErr *domain.Error
	if errors.As(runtimeErr, &domainErr) {
		return domainErr.Code, domainErr.Message
	}
	return domain.CodeRuntimeFailed, runtimeErr.Error()
}

// runSessionID 返回 Run 的 run-scoped ADK 会话标识：会话即运行，
// 终态删除，不承载跨 Run 的持久历史（持久历史归调用方所有）。
func runSessionID(runID string) string {
	return "run:" + runID
}

// normalizeRunRequest 去除标识符、协议与用户消息的首尾空白。Run 入口的
// 所有下游（审计、会话、模型输入）都消费规范化后的值。
func normalizeRunRequest(request RunRequest) RunRequest {
	request.RunID = strings.TrimSpace(request.RunID)
	request.ThreadID = strings.TrimSpace(request.ThreadID)
	request.UserID = strings.TrimSpace(request.UserID)
	request.Protocol = strings.TrimSpace(request.Protocol)
	request.UserMessage = strings.TrimSpace(request.UserMessage)
	return request
}

// validateRunRequest 校验一次执行的入参不变量：三类标识符必须存在且为
// 有界可打印字符，用户消息不得为空，会话历史不得超限。
func validateRunRequest(request RunRequest) error {
	switch {
	case request.RunID == "":
		return domain.NewError(domain.CodeRunIDRequired, "run id is required", domain.ErrInvalidInput)
	case request.ThreadID == "":
		return domain.NewError(domain.CodeThreadIDRequired, "thread id is required", domain.ErrInvalidInput)
	case request.UserID == "":
		return domain.NewError(domain.CodeUserIDRequired, "user id is required", domain.ErrInvalidInput)
	case request.UserMessage == "":
		return domain.NewError(domain.CodeUserMessageRequired, "user message is required", domain.ErrInvalidInput)
	case !ValidIdentifier(request.RunID):
		return domain.NewError(domain.CodeRunIDInvalid, "run id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	case !ValidIdentifier(request.ThreadID):
		return domain.NewError(domain.CodeThreadIDInvalid, "thread id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	case !ValidIdentifier(request.UserID):
		return domain.NewError(domain.CodeUserIDInvalid, "user id must contain at most 128 printable bytes", domain.ErrInvalidInput)
	case len(request.History) > MaxHistoryMessages:
		return domain.NewError(domain.CodeHistoryInvalid, fmt.Sprintf("conversation history exceeds %d messages", MaxHistoryMessages), domain.ErrInvalidInput)
	}
	return nil
}

// Run executes the shared ADK runtime and exposes native ADK events to a
// protocol adapter. It is not a public API and creates no second business path.
func (r *Runtime) Run(
	ctx context.Context,
	request RunRequest,
	observe func(*session.Event) error,
) (result RunResult, runErr error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.ThreadID = strings.TrimSpace(request.ThreadID)
	request.UserID = strings.TrimSpace(request.UserID)
	request.Protocol = strings.TrimSpace(request.Protocol)
	request = normalizeRunRequest(request)
	if err := validateRunRequest(request); err != nil {
		return RunResult{}, err
	}
	if err := r.ensureDurableRunID(ctx, request.RunID); err != nil {
		return RunResult{}, err
	}
	if !r.runs.begin(request.RunID) {
		return RunResult{}, domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
	}
	releaseRun, err := r.acquireRun(ctx)
	if err != nil {
		r.runs.finish(request.RunID)
		return RunResult{}, err
	}
	defer func() {
		releaseRun()
		r.runs.finish(request.RunID)
	}()

	ctx, runSpan := r.tracer.Start(
		ctx,
		"agent.run "+r.entrypoint.Name,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("liki.run.id", request.RunID),
			attribute.String("liki.run.root_id", request.RunID),
			attribute.String("liki.thread.id", request.ThreadID),
			attribute.String("gen_ai.agent.name", r.entrypoint.Name),
			attribute.String("liki.agent.version", r.entrypoint.Version),
			attribute.String("liki.definition.name", r.definition.Metadata.Name),
			attribute.String("liki.definition.version", r.definition.Metadata.Version),
			attribute.String("liki.definition.digest", r.definition.Digest),
		),
	)
	defer runSpan.End()
	startedAt := r.config.Now()
	state := &runState{
		capturePlain: !r.entrypoint.Output.Structured(),
	}
	projector := newEventProjector(r.definition)
	sessionID := runSessionID(request.RunID)
	scope := &llmRunScope{
		runID:             domain.ID(request.RunID),
		threadID:          domain.ID(request.ThreadID),
		userID:            request.UserID,
		protocol:          request.Protocol,
		model:             r.entrypoint.EffectiveModel(r.config.Model),
		instructionDigest: r.entrypoint.InstructionDigest,
		traceID:           traceIDFromContext(ctx),
		spanID:            spanIDFromContext(ctx),
		definitionName:    r.definition.Metadata.Name,
		definitionVersion: r.definition.Metadata.Version,
		definitionDigest:  r.definition.Digest,
		startedAt:         startedAt,
		activeCalls:       make(map[string]llmCallRuntime),
	}
	// 聚合用量在所有退出路径回填（含失败与取消）。
	defer func() { result.Usage = scope.usage() }()
	if !r.llm.begin(sessionID, scope) {
		runErr = domain.NewError(domain.CodeAuditSessionActive, "an audit run is already active for this session", domain.ErrInvalidInput)
		return RunResult{}, runErr
	}
	if auditErr := r.recordRunAudit(ctx, scope, audit.EventRunStarted, audit.StatusRunning, nil); auditErr != nil {
		runErr = auditErr
		auditScope, _ := r.llm.scope(sessionID)
		_, _ = r.llm.end(sessionID, runErr)
		_ = r.delegationAuditor.FailPending(context.WithoutCancel(ctx), auditScope)
		_ = r.toolAuditor.FailPending(context.WithoutCancel(ctx), auditScope)
		_ = r.sessions.Delete(context.WithoutCancel(ctx), &session.DeleteRequest{
			AppName:   r.config.AppName,
			UserID:    request.UserID,
			SessionID: sessionID,
		})
		return RunResult{}, runErr
	}
	defer func() {
		auditCtx := context.WithoutCancel(ctx)
		// 终态证据事务（唯一实现见 run_evidence.go）：终态事件先于任何
		// 进程状态释放。
		if err := r.closeRunEvidence(auditCtx, scope, runErr); err != nil && runErr == nil {
			runErr = err
		}
		// 终态事件已尝试落盘；此后释放进程状态。清理失败属运维噪音，
		// 不改变已持久化的执行结果。
		r.llm.forget(sessionID)
		if deleteErr := r.sessions.Delete(auditCtx, &session.DeleteRequest{
			AppName:   r.config.AppName,
			UserID:    request.UserID,
			SessionID: sessionID,
		}); deleteErr != nil {
			r.config.Logger.WarnContext(ctx, "agent_run_session_cleanup_failed",
				"run_id", request.RunID,
				"error", runtimeError(deleteErr).Error(),
			)
		}
		if runErr != nil {
			runtimeErr := runtimeError(runErr)
			runSpan.RecordError(runtimeErr)
			runSpan.SetStatus(codes.Error, runtimeErr.Error())
		}
	}()
	r.config.Logger.InfoContext(ctx, "agent_run_started",
		append(traceLogFields(ctx),
			"run_id", request.RunID,
			"thread_id", request.ThreadID,
			"user_id", request.UserID,
			"model", r.entrypoint.EffectiveModel(r.config.Model),
		)...)

	events := r.runner.Run(
		ctx,
		request.UserID,
		sessionID,
		buildUserContent(request),
		agent.RunConfig{StreamingMode: agent.StreamingModeSSE},
	)
	for event, err := range events {
		if err != nil {
			runSpan.RecordError(err)
			runSpan.SetStatus(codes.Error, runtimeError(err).Error())
			r.config.Logger.WarnContext(ctx, "agent_run_failed",
				append(traceLogFields(ctx),
					"run_id", request.RunID, "error", runtimeError(err).Error(),
				)...)
			runErr = runtimeError(err)
			return RunResult{}, runErr
		}
		state.consume(event)
		// Task-mode sub-agents stop their event iterator on finish_task, so
		// their AfterAgent callback never runs; complete the delegation from
		// the synthesized task-tool function response instead.
		if delegationErr := r.delegationAuditor.CompleteByToolResponse(ctx, scope, event); delegationErr != nil {
			runSpan.RecordError(delegationErr)
			runSpan.SetStatus(codes.Error, runtimeError(delegationErr).Error())
			runErr = runtimeError(delegationErr)
			return RunResult{}, runErr
		}
		if observe != nil {
			if visible, visibleOK := projector.Project(event); visibleOK {
				if err := observe(visible); err != nil {
					runSpan.RecordError(err)
					runSpan.SetStatus(codes.Error, runtimeError(err).Error())
					runErr = runtimeError(err)
					return RunResult{}, runErr
				}
			}
		}
	}
	if r.entrypoint.Output.Structured() && state.output.JSON == nil {
		if err := r.loadStructuredAnalysis(ctx, request, sessionID, state); err != nil {
			runSpan.RecordError(err)
			runSpan.SetStatus(codes.Error, runtimeError(err).Error())
			runErr = runtimeError(err)
			return RunResult{}, runErr
		}
	}

	if strings.TrimSpace(state.output.Text) == "" {
		r.config.Logger.Warn("agent_run_empty_response", "run_id", request.RunID)
		runErr = domain.NewError(domain.CodeRuntimeEmptyResponse, "ADK runtime returned no answer text", nil)
		return RunResult{}, runErr
	}
	runSpan.SetStatus(codes.Ok, "")
	r.config.Logger.InfoContext(ctx, "agent_run_completed",
		append(traceLogFields(ctx),
			"run_id", request.RunID,
			"agent", r.entrypoint.Name,
			"tools_used", state.tools.Names(),
			"model", r.entrypoint.EffectiveModel(r.config.Model),
		)...)

	return RunResult{
		Definition: r.definition.DefinitionRef(),
		Output:     state.output.JSON,
		Text:       state.output.Text,
	}, nil
}

func (r *Runtime) ensureDurableRunID(ctx context.Context, runID string) error {
	checker, ok := r.config.AuditRecorder.(audit.RunExistenceChecker)
	if !ok {
		return nil
	}
	exists, err := checker.RunExists(ctx, domain.ID(runID))
	if err != nil {
		return err
	}
	if exists {
		return domain.NewError(domain.CodeRunIDConflict, "run id is already active or completed", domain.ErrInvalidInput)
	}
	return nil
}

func (r *Runtime) recordRunAudit(
	ctx context.Context,
	scope *llmRunScope,
	eventType audit.EventType,
	status audit.Status,
	cause error,
) error {
	event := r.runEvent(ctx, scope, eventType, status, cause)
	if err := event.Validate(); err != nil {
		return err
	}
	return r.recordAuditWithRetry(ctx, &event)
}

func (r *Runtime) recordAuditWithRetry(ctx context.Context, event *audit.Event) error {
	err := r.config.AuditRecorder.Record(ctx, event)
	if err == nil || !retryableAuditAppend(err) {
		return err
	}
	// SQLite busy and transient exporter failures are the common case. A
	// second append attempt cannot overwrite the append-only event ID.
	return r.config.AuditRecorder.Record(ctx, event)
}

// retryableAuditAppend reports whether a failed append is worth an immediate
// second attempt. Event-contract violations wrap domain.ErrInvalidInput and
// conflicts are deterministic: retrying either cannot change the outcome.
// Infrastructure failures (busy, timeout, exporter) remain retryable.
func retryableAuditAppend(err error) bool {
	if errors.Is(err, domain.ErrInvalidInput) {
		return false
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) && domainErr.Code == audit.CodeAuditEventConflict {
		return false
	}
	return true
}

// loadStructuredAnalysis consumes the parsed model output from ADK session
// state. Event.Output is deliberately cleared by ADK Runner before yielding,
// so session state is the only framework-supported source.
func (r *Runtime) loadStructuredAnalysis(ctx context.Context, request RunRequest, sessionID string, state *runState) error {
	response, err := r.sessions.Get(ctx, &session.GetRequest{
		AppName:   r.config.AppName,
		UserID:    request.UserID,
		SessionID: sessionID,
	})
	if err != nil {
		r.config.Logger.Warn("agent_session_state_unavailable",
			"run_id", request.RunID, "error", runtimeError(err).Error())
		return domain.NewError(domain.CodeRuntimeSessionUnavailable, "read ADK session state", err)
	}
	if response == nil || response.Session == nil {
		return nil
	}
	value, err := response.Session.State().Get(StructuredOutputStateKey(r.entrypoint.Name))
	if err != nil {
		return nil
	}
	return state.consumeStructuredOutput(value, r.entrypoint)
}

// Entrypoint exposes the selected AgentDefinition to protocol adapters.
func (r *Runtime) Entrypoint() *AgentDefinition {
	clone := cloneAgentDefinition(r.entrypoint)
	return &clone
}

// Deployment exposes non-secret deployment metadata for standard protocol
// discovery. Instructions and schemas remain internal to the runtime.
func (r *Runtime) Deployment() *Deployment {
	deployment := *r.definition
	deployment.Spec.MCPServers = append([]MCPServerDefinition(nil), r.definition.Spec.MCPServers...)
	deployment.Spec.Agents = make([]AgentDefinition, len(r.definition.Spec.Agents))
	for index := range r.definition.Spec.Agents {
		deployment.Spec.Agents[index] = cloneAgentDefinition(&r.definition.Spec.Agents[index])
	}
	return &deployment
}

func cloneAgentDefinition(definition *AgentDefinition) AgentDefinition {
	clone := *definition
	clone.SubAgents = append([]AgentReference(nil), definition.SubAgents...)
	clone.Tools.Allow = make(map[string][]string, len(definition.Tools.Allow))
	for server, tools := range definition.Tools.Allow {
		clone.Tools.Allow[server] = append([]string(nil), tools...)
	}
	return clone
}

func traceLogFields(ctx context.Context) []any {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return nil
	}
	return []any{"trace_id", spanContext.TraceID().String(), "span_id", spanContext.SpanID().String()}
}

func traceIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func spanIDFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}
