// Package a2a exposes the ADK through the official A2A protocol
// binding. There is no private agent API behind this adapter.
package a2a

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ml8s/agent-runtime/internal/execution"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/ml8s/agent-runtime/internal/agent"
	observability "github.com/ml8s/agent-runtime/internal/observability"
	platformconfig "github.com/ml8s/agent-runtime/internal/platform/config"
	"github.com/ml8s/agent-runtime/internal/platform/identity"
	"google.golang.org/adk/v2/server/adka2a/v2"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const (
	// Path is the canonical JSON-RPC binding declared by the Agent Card.
	Path = "/a2a"
)

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

	// ServicePrincipal is the authenticated A2A protocol principal represented by the
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
		config.ServicePrincipal = "agent-runtime-a2a"
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
		GenAIPartConverter: func(ctx context.Context, event *session.Event, part *genai.Part) (*a2a.Part, error) {
			// The entrypoint is immutable after runtime construction; capture it
			// once instead of deep-cloning the definition for every streamed part.
			// Model-text visibility is resolved per event author against the
			// deployment, mirroring the AG-UI projector. A structured expert
			// delegated under a plain entrypoint must not leak raw JSON.
			exposesText := func(author string) bool {
				definition, ok := deployment.Agent(author)
				return ok && deploymentpkg.ExposesModelText(&definition.AgentDefinition)
			}
			attemptID, _ := ctx.Value(executionAttemptKey{}).(string)
			structured, applies, err := server.structuredOutput(attemptID, event, resolvedEntrypoint)
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
			handle := agent.AuditAttemptHandle{
				ADKSessionID: request.ContextID,
				AttemptID:    attempt.ID,
			}
			scope := agent.RunAuditIdentity{
				RunID:     attempt.ID,
				ThreadID:  request.ContextID,
				UserID:    caller,
				Protocol:  "a2a",
				TaskID:    attempt.TaskID,
				MessageID: attempt.MessageID,
			}
			ctx = agent.WithAuditAttempt(ctx, attempt.ID)
			ctx = context.WithValue(ctx, executionAttemptKey{}, attempt.ID)
			ctx, cancelExecution := context.WithTimeout(ctx, config.RunTimeout)
			ctx = context.WithValue(ctx, executionCancelKey{}, cancelExecution)
			if err := runtime.BeginAuditRun(ctx, handle, scope); err != nil {
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
			attempt := executionAttemptFromContext(ctx, ctx.RequestContext())
			return runtime.EndAuditRun(ctx, agent.AuditAttemptHandle{
				ADKSessionID: attempt.ContextID,
				AttemptID:    attempt.ID,
			}, err, options...)
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
			attempt := executionAttemptFromRequest(request).withID()
			server.structured.release(attempt.ID)
			if auditErr := runtime.EndAuditRun(ctx, agent.AuditAttemptHandle{
				ADKSessionID: attempt.ContextID,
				AttemptID:    attempt.ID,
			}, cause, options...); auditErr != nil {
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
