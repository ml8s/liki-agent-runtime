// Package a2a exposes the Liki ADK runtime through the official A2A protocol
// binding. There is no private agent API behind this adapter.
package a2a

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/execution"

	deploymentpkg "github.com/ml8s/liki-agent-runtime/internal/deployment"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/ml8s/liki-agent-runtime/internal/agent"
	observability "github.com/ml8s/liki-agent-runtime/internal/observability"
	platformconfig "github.com/ml8s/liki-agent-runtime/internal/platform/config"
	"github.com/ml8s/liki-agent-runtime/internal/platform/identity"
	"google.golang.org/adk/v2/server/adka2a/v2"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const (
	// Path is the canonical JSON-RPC binding declared by the Agent Card.
	Path = "/a2a"
)

type executionCancelKey struct{}

type executionAttemptKey struct{}

type executionAttempt struct {
	ID        string
	TaskID    string
	ContextID string
	MessageID string
}

func executionAttemptFromRequest(request *a2asrv.ExecutorContext) executionAttempt {
	if request == nil {
		return executionAttempt{}
	}
	messageID := ""
	if request.Message != nil {
		messageID = request.Message.ID
	}
	return executionAttempt{
		TaskID:    string(request.TaskID),
		ContextID: request.ContextID,
		MessageID: messageID,
	}
}

func (a executionAttempt) withID() executionAttempt {
	digest := sha256.Sum256([]byte(a.TaskID + "\x00" + a.ContextID + "\x00" + a.MessageID))
	a.ID = hex.EncodeToString(digest[:])
	return a
}

func executionAttemptFromContext(ctx context.Context, request *a2asrv.ExecutorContext) executionAttempt {
	if attemptID, ok := ctx.Value(executionAttemptKey{}).(string); ok && attemptID != "" {
		attempt := executionAttemptFromRequest(request)
		attempt.ID = attemptID
		return attempt
	}
	return executionAttemptFromRequest(request).withID()
}

func securitySchemes(config SecurityConfig) a2a.NamedSecuritySchemes {
	schemes := a2a.NamedSecuritySchemes{}
	if config.Bearer {
		schemes["liki_service_bearer"] = a2a.HTTPAuthSecurityScheme{
			Scheme:       "Bearer",
			BearerFormat: "opaque",
			Description:  "Liki internal service bearer token",
		}
	}
	if config.MutualTLS {
		schemes["liki_service_mtls"] = a2a.MutualTLSSecurityScheme{
			Description: "Liki verified client-certificate channel",
		}
	}
	return schemes
}

func securityRequirements(config SecurityConfig) a2a.SecurityRequirementsOptions {
	var options a2a.SecurityRequirementsOptions
	if config.Bearer {
		options = append(options, a2a.SecurityRequirements{"liki_service_bearer": {}})
	}
	if config.MutualTLS {
		options = append(options, a2a.SecurityRequirements{"liki_service_mtls": {}})
	}
	return options
}

func init() {
	// ADK's in-process A2A artifact path copies Part.Data with gob. Preserve
	// JSON numbers lexically instead of coercing them through float64.
	gob.Register(json.Number(""))
}

// Config owns the public transport declaration for A2A discovery.
type Config struct {
	PublicURL  *url.URL
	RunTimeout time.Duration
	Logger     *slog.Logger
	Metrics    observability.A2AMetrics

	// ServicePrincipal is the authenticated A2A caller represented by the
	// transport's service credential. A shared bearer token or a verified mTLS
	// listener is one protocol principal, even when the gateway serves many
	// end users behind it.
	ServicePrincipal string

	// Security declares the transport authentication schemes actually accepted
	// by the composed listener. An empty declaration is valid only for an
	// explicitly unauthenticated development profile.
	Security SecurityConfig

	// Push enables the standard A2A push notification capability only when
	// both standard dependencies are present. A partial configuration fails
	// startup instead of advertising a capability the handler cannot serve.
	Push PushConfig

	// TaskStore is the official A2A taskstore.Store injection point. A shared
	// database implementation can be supplied without changing A2A wire
	// semantics. Nil selects the SDK's official in-memory implementation.
	TaskStore taskstore.Store
}

// SecurityConfig describes actual transport authentication capabilities.
type SecurityConfig struct {
	Bearer    bool
	MutualTLS bool
}

// PushConfig owns the official A2A push notification dependencies.
type PushConfig struct {
	Store  push.ConfigStore
	Sender push.Sender
}

func (c PushConfig) enabled() bool {
	return c.Store != nil && c.Sender != nil
}

func (c PushConfig) partial() bool {
	return c.Store != nil || c.Sender != nil
}

// Server is the official ADK/A2A executor and Agent Card provider.
type Server struct {
	endpoint   http.Handler
	discovery  http.Handler
	structured structuredMemo
}

// structuredMemo parses one final event's structured output once. The part
// converter is invoked per content part, and every part of the same event
// would otherwise repeat the identical decode/validate/canonicalize pipeline.
// The single entry is keyed by event pointer and mutex-guarded because one
// server serves concurrent tasks; interleaved events at worst recompute, never
// return a stale mismatch.
type structuredMemo struct {
	mu     sync.Mutex
	event  *session.Event
	output execution.OutputResult
	err    error
	valid  bool

	// parse is a test seam; production always uses execution.ParseStructuredOutput.
	parse func(value any, definition *deploymentpkg.ResolvedAgentDefinition) (execution.OutputResult, error)
}

// structuredOutput returns the memoized parse when the event's state delta
// selects the structured branch. applies reports whether the branch matches.
func (s *Server) structuredOutput(
	event *session.Event,
	entrypoint *deploymentpkg.ResolvedAgentDefinition,
) (output execution.OutputResult, applies bool, err error) {
	if event == nil || entrypoint == nil ||
		event.Author != entrypoint.Name || !event.IsFinalResponse() {
		return execution.OutputResult{}, false, nil
	}
	value, ok := event.Actions.StateDelta[execution.StructuredOutputStateKey(entrypoint.Name)]
	if !ok {
		return execution.OutputResult{}, false, nil
	}
	s.structured.mu.Lock()
	if s.structured.valid && s.structured.event == event {
		output, err = s.structured.output, s.structured.err
		s.structured.mu.Unlock()
		return output, true, err
	}
	s.structured.mu.Unlock()

	output, err = s.structured.parse(value, entrypoint)
	s.structured.mu.Lock()
	s.structured.event = event
	s.structured.output = output
	s.structured.err = err
	s.structured.valid = true
	s.structured.mu.Unlock()
	return output, true, err
}

// New wires the ADK runtime directly into the official A2A server stack.
func New(runtime *agent.Runtime, config Config) (*Server, error) {
	if runtime == nil {
		return nil, fmt.Errorf("adk runtime is required")
	}
	if config.PublicURL == nil {
		return nil, fmt.Errorf("public url is required")
	}
	if config.RunTimeout <= 0 {
		config.RunTimeout = platformconfig.DefaultRunTimeout
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if strings.TrimSpace(config.ServicePrincipal) == "" {
		config.ServicePrincipal = "liki-a2a"
	}
	if config.Push.partial() && !config.Push.enabled() {
		return nil, fmt.Errorf("a2a push notifications require both config store and sender")
	}
	server := &Server{structured: structuredMemo{parse: execution.ParseStructuredOutput}}
	root := runtime.RootAgent()
	deployment := runtime.AgentDeployment()
	entrypoint := runtime.Entrypoint()
	resolvedEntrypoint := runtime.ResolvedEntrypoint()
	outputModes := []string{"text/plain"}
	if entrypoint.Output.Structured() {
		outputModes = append(outputModes, "application/json")
	}
	capabilities := a2a.AgentCapabilities{
		Streaming:         true,
		PushNotifications: config.Push.enabled(),
	}
	card := a2a.AgentCard{
		Name:        root.Name(),
		Description: root.Description(),
		Version:     runtime.Entrypoint().Version,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(config.PublicURL.JoinPath(Path).String(), a2a.TransportProtocolJSONRPC),
		},
		Capabilities:         capabilities,
		SecuritySchemes:      securitySchemes(config.Security),
		SecurityRequirements: securityRequirements(config.Security),
		DefaultInputModes:    []string{"text/plain"},
		DefaultOutputModes:   outputModes,
		Skills:               safeAgentSkills(deployment),
	}

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runtime.RunnerConfig(),
		GenAIPartConverter: func(_ context.Context, event *session.Event, part *genai.Part) (*a2a.Part, error) {
			// The entrypoint is immutable after runtime construction; capture it
			// once instead of deep-cloning the definition for every streamed part.
			// Model-text visibility is resolved per event author against the
			// deployment, mirroring the AG-UI projector. A structured expert
			// delegated under a plain entrypoint must not leak raw JSON.
			exposesText := func(author string) bool {
				definition, ok := deployment.Agent(author)
				return ok && deploymentpkg.ExposesModelText(&definition.AgentDefinition)
			}
			structured, applies, err := server.structuredOutput(event, resolvedEntrypoint)
			if applies && err != nil {
				return nil, err
			}
			var structuredPointer *execution.OutputResult
			if !applies {
				structuredPointer = nil
			} else {
				structuredPointer = &structured
			}
			return agentPart(event, part, exposesText, resolvedEntrypoint, structuredPointer)
		},
		BeforeExecuteCallback: func(ctx context.Context, request *a2asrv.ExecutorContext) (context.Context, error) {
			caller := ""
			if verified, ok := identity.UserIDFromContext(ctx); ok {
				caller = verified
			}
			if caller == "" && request.User != nil && request.User.Authenticated {
				caller = request.User.Name
			}
			if caller == "" {
				caller = "a2a:" + string(request.TaskID)
			}
			attempt := executionAttemptFromRequest(request).withID()
			scope := agent.RunAuditIdentity{
				RunID:     attempt.ID,
				ThreadID:  request.ContextID,
				UserID:    caller,
				Protocol:  "a2a",
				TaskID:    attempt.TaskID,
				MessageID: attempt.MessageID,
			}
			ctx = agent.WithAuditAttempt(ctx, attempt.ID)
			ctx, cancelExecution := context.WithTimeout(ctx, config.RunTimeout)
			ctx = context.WithValue(ctx, executionCancelKey{}, cancelExecution)
			if err := runtime.BeginAuditRun(ctx, request.ContextID, scope); err != nil {
				cancelExecution()
				return nil, err
			}
			return ctx, nil
		},
		AfterExecuteCallback: func(ctx adka2a.ExecutorContext, finalEvent *a2a.TaskStatusUpdateEvent, err error) error {
			terminal := finalEvent != nil && finalEvent.Status.State.Terminal()
			if terminal {
				cancelExecution(ctx)
			}
			var options []agent.AuditEndOption
			switch {
			case finalEvent != nil && finalEvent.Status.State == a2a.TaskStateCanceled:
				options = append(options, agent.WithCanceledAttempt())
			case finalEvent != nil && !terminal:
				options = append(options, agent.WithPausedAttempt())
			}
			// A2A ContextID owns the ADK session; only an attempt audit ends here.
			options = append(options, agent.WithPreservedSession())
			return runtime.EndAuditRun(ctx, executionAttemptFromContext(ctx, ctx.RequestContext()).ID, err, options...)
		},
		A2AExecutionCleanupCallback: func(ctx context.Context, request *a2asrv.ExecutorContext, _ []*a2a.AgentCard, result a2a.SendMessageResult, cause error) {
			cancelExecution(ctx)
			var options []agent.AuditEndOption
			if task, ok := result.(*a2a.Task); ok {
				switch {
				case task.Status.State == a2a.TaskStateCanceled:
					options = append(options, agent.WithCanceledAttempt())
				case !task.Status.State.Terminal():
					options = append(options, agent.WithPausedAttempt())
				}
			}
			options = append(options, agent.WithPreservedSession())
			if auditErr := runtime.EndAuditRun(ctx, executionAttemptFromRequest(request).withID().ID, cause, options...); auditErr != nil {
				config.Logger.WarnContext(ctx, "a2a_cleanup_audit_failed",
					"task_id", request.TaskID,
					"context_id", request.ContextID,
					"error", auditErr.Error(),
				)
			}
		},
	})
	handlerOptions := []a2asrv.RequestHandlerOption{
		a2asrv.WithCapabilityChecks(&capabilities),
		a2asrv.WithCallInterceptors(authenticatedPrincipalInterceptor{principal: config.ServicePrincipal}),
	}
	if config.Push.enabled() {
		handlerOptions = append(
			handlerOptions,
			a2asrv.WithPushNotifications(
				newMeasuredPushConfigStore(config.Push.Store, config.Metrics),
				withMeasuredPushSender(config.Push.Sender, config.Metrics),
			),
		)
	}
	if config.TaskStore != nil {
		handlerOptions = append(handlerOptions, a2asrv.WithTaskStore(config.TaskStore))
	}
	handlerOptions = append(handlerOptions, a2asrv.WithAgentInactivityTimeout(config.RunTimeout))
	requestHandler := a2asrv.NewHandler(
		executor,
		handlerOptions...,
	)

	discovery := a2asrv.NewStaticAgentCardHandler(&card)
	endpoint := a2asrv.NewJSONRPCHandler(requestHandler)
	server.endpoint = endpoint
	server.discovery = discovery
	return server, nil
}

func cancelExecution(ctx context.Context) {
	if cancel, ok := ctx.Value(executionCancelKey{}).(context.CancelFunc); ok {
		cancel()
	}
}

// authenticatedPrincipalInterceptor maps the transport's service credential to
// the official A2A call context. Task and push-config ownership therefore use
// an authenticated principal instead of the SDK's anonymous zero value.
type authenticatedPrincipalInterceptor struct {
	principal string
}

func (i authenticatedPrincipalInterceptor) Before(
	ctx context.Context,
	callCtx *a2asrv.CallContext,
	_ *a2asrv.Request,
) (context.Context, any, error) {
	if callCtx == nil {
		return ctx, nil, nil
	}
	if callCtx.User == nil || !callCtx.User.Authenticated {
		attrs := map[string]any{}
		if verified, ok := identity.FromContext(ctx); ok {
			if verified.UserID != "" {
				i.principal = verified.UserID
			}
			if verified.Tenant != "" {
				attrs["tenant"] = verified.Tenant
			}
		}
		callCtx.User = a2asrv.NewAuthenticatedUser(i.principal, attrs)
	}
	return ctx, nil, nil
}

func (authenticatedPrincipalInterceptor) After(context.Context, *a2asrv.CallContext, *a2asrv.Response) error {
	return nil
}

// validatingPushConfigStore enforces the callback policy before a webhook can
// enter storage. The SDK sender additionally validates resolved addresses at
// dial time, which remains the authoritative SSRF boundary.
type validatingPushConfigStore struct {
	delegate push.ConfigStore
	metrics  observability.A2AMetrics
}

func newMeasuredPushConfigStore(delegate push.ConfigStore, metrics observability.A2AMetrics) push.ConfigStore {
	return validatingPushConfigStore{delegate: delegate, metrics: metrics}
}

func observePushConfig(metrics observability.A2AMetrics, operation string, err error) {
	if metrics == nil {
		return
	}
	status := "success"
	switch {
	case errors.Is(err, push.ErrPushConfigNotFound), errors.Is(err, a2a.ErrTaskNotFound):
		status = "not_found"
	case errors.Is(err, a2a.ErrInvalidParams), errors.Is(err, a2a.ErrInvalidRequest):
		status = "invalid"
	case err != nil:
		status = "error"
	}
	metrics.ObservePushConfig(operation, status)
}

type measuredPushSender struct {
	delegate push.Sender
	metrics  observability.A2AMetrics
}

func withMeasuredPushSender(delegate push.Sender, metrics observability.A2AMetrics) push.Sender {
	return measuredPushSender{delegate: delegate, metrics: metrics}
}

func (s measuredPushSender) SendPush(ctx context.Context, config *a2a.PushConfig, event a2a.Event) error {
	started := time.Now()
	err := s.delegate.SendPush(ctx, config, event)
	if s.metrics != nil {
		status := "success"
		if err != nil {
			status = "error"
		}
		s.metrics.ObservePushDelivery(status, time.Since(started))
	}
	return err
}

func (s validatingPushConfigStore) Save(
	ctx context.Context,
	taskID a2a.TaskID,
	config *a2a.PushConfig,
) (*a2a.PushConfig, error) {
	if err := validatePushConfig(taskID, config); err != nil {
		observePushConfig(s.metrics, "save", err)
		return nil, err
	}
	saved, err := s.delegate.Save(ctx, taskID, config)
	observePushConfig(s.metrics, "save", err)
	return saved, err
}

func (s validatingPushConfigStore) Get(
	ctx context.Context,
	taskID a2a.TaskID,
	configID string,
) (*a2a.PushConfig, error) {
	config, err := s.delegate.Get(ctx, taskID, configID)
	observePushConfig(s.metrics, "get", err)
	return config, err
}

func (s validatingPushConfigStore) List(
	ctx context.Context,
	taskID a2a.TaskID,
) ([]*a2a.PushConfig, error) {
	configs, err := s.delegate.List(ctx, taskID)
	observePushConfig(s.metrics, "list", err)
	return configs, err
}

func (s validatingPushConfigStore) Delete(
	ctx context.Context,
	taskID a2a.TaskID,
	configID string,
) error {
	err := s.delegate.Delete(ctx, taskID, configID)
	observePushConfig(s.metrics, "delete", err)
	return err
}

func (s validatingPushConfigStore) DeleteAll(ctx context.Context, taskID a2a.TaskID) error {
	err := s.delegate.DeleteAll(ctx, taskID)
	observePushConfig(s.metrics, "delete_all", err)
	return err
}

func validatePushConfig(taskID a2a.TaskID, config *a2a.PushConfig) error {
	if taskID == "" || config == nil || config.URL == "" {
		return fmt.Errorf("%w: task id and push URL are required", a2a.ErrInvalidParams)
	}
	parsed, err := url.Parse(config.URL)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Opaque != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf("%w: push URL must be an HTTPS URL without credentials or fragment", a2a.ErrInvalidParams)
	}
	if config.Auth == nil {
		return nil
	}
	switch strings.ToLower(config.Auth.Scheme) {
	case "basic", "bearer":
		if config.Auth.Credentials == "" {
			return fmt.Errorf("%w: push auth credentials are required", a2a.ErrInvalidParams)
		}
	default:
		return fmt.Errorf("%w: unsupported push auth scheme", a2a.ErrInvalidParams)
	}
	return nil
}

// safeAgentSkills builds standard A2A skills from curated deployment metadata.
// ADK's default skill builder derives descriptions from instructions, which
// would turn prompt material into public discovery data.
func safeAgentSkills(deployment *deploymentpkg.AgentDeployment) []a2a.AgentSkill {
	entrypoint, err := deployment.EntrypointDefinition()
	if err != nil {
		return nil
	}
	definition := &deployment.Spec.Agents[0]
	for index := range deployment.Spec.Agents {
		if deployment.Spec.Agents[index].Name == entrypoint.Name {
			definition = &deployment.Spec.Agents[index]
			break
		}
	}
	tags := []string{"entrypoint"}
	if definition.Output.Structured() {
		tags = append(tags, "structured-output")
	}
	return []a2a.AgentSkill{{
		ID:          definition.Name,
		Name:        definition.Name,
		Description: definition.Description,
		Tags:        tags,
	}}
}

// AgentCardHandler serves the standard well-known discovery endpoint.
func (s *Server) AgentCardHandler() http.Handler {
	return s.discovery
}

// EndpointHandler serves the standard JSON-RPC protocol endpoint.
func (s *Server) EndpointHandler() http.Handler {
	return s.endpoint
}

// agentPart maps native ADK parts onto A2A artifact parts. Model-text
// visibility is decided per event author by exposesText: plain Authors stream
// their text, structured or unknown Authors never emit raw model text. Only a
// structured entrypoint's validated final answer becomes a standard
// application/json data part; the user-facing text is the JSON field selected
// by its JSON Pointer, and no private metadata is carried on the wire.
func agentPart(
	event *session.Event,
	part *genai.Part,
	exposesText func(author string) bool,
	entrypoint *deploymentpkg.ResolvedAgentDefinition,
	structured *execution.OutputResult,
) (*a2a.Part, error) {
	if part == nil {
		return nil, nil
	}
	if part.FunctionCall != nil || part.FunctionResponse != nil {
		var longRunningToolIDs []string
		if event != nil {
			longRunningToolIDs = event.LongRunningToolIDs
		}
		return adka2a.ToA2APart(part, longRunningToolIDs)
	}
	if event != nil && exposesText != nil && exposesText(event.Author) {
		if strings.TrimSpace(part.Text) == "" {
			return nil, nil
		}
		var longRunningToolIDs []string
		if event != nil {
			longRunningToolIDs = event.LongRunningToolIDs
		}
		return adka2a.ToA2APart(part, longRunningToolIDs)
	}
	if event != nil && entrypoint != nil && event.Author == entrypoint.Name && event.IsFinalResponse() {
		value, ok := event.Actions.StateDelta[execution.StructuredOutputStateKey(entrypoint.Name)]
		if ok {
			var output execution.OutputResult
			if structured != nil {
				// The server already parsed this event once for a prior part.
				output = *structured
			} else {
				parsed, err := execution.ParseStructuredOutput(value, entrypoint)
				if err != nil {
					return nil, err
				}
				output = parsed
			}
			var data map[string]any
			if document, ok := output.Document.(map[string]any); ok {
				// The retained document is exactly what decoding the canonical
				// JSON with UseNumber would produce; reuse it without a second
				// decode of the same bytes.
				data = document
			} else {
				decoder := json.NewDecoder(bytes.NewReader(output.JSON))
				decoder.UseNumber()
				if err := decoder.Decode(&data); err != nil {
					return nil, fmt.Errorf("decode structured A2A data: %w", err)
				}
			}
			dataPart := a2a.NewDataPart(data)
			dataPart.MediaType = "application/json"
			return dataPart, nil
		}
	}
	return nil, nil
}
