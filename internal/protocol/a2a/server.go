// Package a2a exposes the Liki ADK runtime through the official A2A protocol
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

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/ml8s/liki-agent-runtime/internal/agent"
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

	// TaskStore is the official A2A taskstore.Store injection point. A shared
	// database implementation can be supplied without changing A2A wire
	// semantics. Nil selects the SDK's official in-memory implementation.
	TaskStore taskstore.Store
}

// Server is the official ADK/A2A executor and Agent Card provider.
type Server struct {
	endpoint  http.Handler
	discovery http.Handler
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
	root := runtime.RootAgent()
	deployment := runtime.Deployment()
	entrypoint := runtime.Entrypoint()
	outputModes := []string{"text/plain"}
	if entrypoint.Output.Structured() {
		outputModes = append(outputModes, "application/json")
	}
	capabilities := a2a.AgentCapabilities{Streaming: true}
	securityScheme := a2a.HTTPAuthSecurityScheme{
		Scheme:       "Bearer",
		BearerFormat: "opaque",
		Description:  "Liki internal service bearer token",
	}
	card := a2a.AgentCard{
		Name:        root.Name(),
		Description: root.Description(),
		Version:     runtime.Entrypoint().Version,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(config.PublicURL.JoinPath(Path).String(), a2a.TransportProtocolJSONRPC),
		},
		Capabilities: capabilities,
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"liki_service_bearer": securityScheme,
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{
			{"liki_service_bearer": {}},
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: outputModes,
		Skills:             safeAgentSkills(deployment),
	}

	executor := adka2a.NewExecutor(adka2a.ExecutorConfig{
		RunnerConfig: runtime.RunnerConfig(),
		GenAIPartConverter: func(_ context.Context, event *session.Event, part *genai.Part) (*a2a.Part, error) {
			entrypoint := runtime.Entrypoint()
			// Model-text visibility is resolved per event author against the
			// deployment, mirroring the AG-UI projector. A structured expert
			// delegated under a plain entrypoint must not leak raw JSON.
			exposesText := func(author string) bool {
				definition, ok := deployment.Agent(author)
				return ok && agent.ExposesModelText(definition)
			}
			return agentPart(event, part, exposesText, entrypoint)
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
			scope := agent.AuditRunScope{
				RunID:    string(request.TaskID),
				ThreadID: request.ContextID,
				UserID:   caller,
				Protocol: "a2a",
			}
			ctx, cancelExecution := context.WithTimeout(ctx, config.RunTimeout)
			ctx = context.WithValue(ctx, executionCancelKey{}, cancelExecution)
			if err := runtime.BeginAuditRun(ctx, request.ContextID, scope); err != nil {
				cancelExecution()
				return nil, err
			}
			return ctx, nil
		},
		AfterExecuteCallback: func(ctx adka2a.ExecutorContext, _ *a2a.TaskStatusUpdateEvent, err error) error {
			cancelExecution(ctx)
			return runtime.EndAuditRun(ctx, ctx.SessionID(), err)
		},
		A2AExecutionCleanupCallback: func(ctx context.Context, request *a2asrv.ExecutorContext, _ []*a2a.AgentCard, _ a2a.SendMessageResult, cause error) {
			cancelExecution(ctx)
			if auditErr := runtime.EndAuditRun(ctx, request.ContextID, cause); auditErr != nil {
				config.Logger.WarnContext(ctx, "a2a_cleanup_audit_failed",
					"task_id", request.TaskID,
					"context_id", request.ContextID,
					"error", auditErr.Error(),
				)
			}
		},
	})
	handlerOptions := []a2asrv.RequestHandlerOption{a2asrv.WithCapabilityChecks(&capabilities)}
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
	return &Server{endpoint: endpoint, discovery: discovery}, nil
}

func cancelExecution(ctx context.Context) {
	if cancel, ok := ctx.Value(executionCancelKey{}).(context.CancelFunc); ok {
		cancel()
	}
}

// safeAgentSkills builds standard A2A skills from curated deployment metadata.
// ADK's default skill builder derives descriptions from instructions, which
// would turn prompt material into public discovery data.
func safeAgentSkills(deployment *agent.Deployment) []a2a.AgentSkill {
	entrypoint, err := deployment.EntrypointDefinition()
	if err != nil {
		return nil
	}
	skills := make([]a2a.AgentSkill, 0, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		definition := &deployment.Spec.Agents[index]
		isEntrypoint := definition.Name == entrypoint.Name
		tags := []string{
			"adk:llm-agent",
			"mode:" + string(definition.Mode),
		}
		if isEntrypoint {
			tags = append(tags, "entrypoint")
		}
		for _, reference := range definition.Tools.References() {
			tags = append(tags, "tool:"+reference.Server+"/"+reference.Name)
		}
		if definition.Output.Structured() {
			tags = append(tags, "structured-output")
		}
		skills = append(skills, a2a.AgentSkill{
			ID:          definition.Name,
			Name:        definition.Name,
			Description: definition.Description,
			Tags:        tags,
		})
	}
	return skills
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
	entrypoint *agent.AgentDefinition,
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
		value, ok := event.Actions.StateDelta[agent.StructuredOutputStateKey(entrypoint.Name)]
		if ok {
			output, err := agent.ParseStructuredOutput(value, entrypoint)
			if err != nil {
				return nil, err
			}
			var data map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(output.JSON)))
			decoder.UseNumber()
			if err := decoder.Decode(&data); err != nil {
				return nil, fmt.Errorf("decode structured A2A data: %w", err)
			}
			dataPart := a2a.NewDataPart(data)
			dataPart.MediaType = "application/json"
			return dataPart, nil
		}
	}
	return nil, nil
}
