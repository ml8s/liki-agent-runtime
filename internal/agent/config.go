package agent

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
)

// Config validates and wires the runtime's dependencies.
type Config struct {
	AppName          string
	Model            string
	ModelBaseURL     string
	ModelAPIKey      string
	ModelTimeout     time.Duration
	Temperature      float64
	AuditRecorder    audit.Recorder
	Metrics          Metrics
	Provider         string
	StructuredOutput string
	// MaxOutputTokens bounds every model response; 0 leaves the provider
	// default unbounded.
	MaxOutputTokens int
	Deployment      *Deployment

	// SkillsRoot overrides every agent's skills.root from the deployment so
	// that development host and container layouts can point at the same
	// fixture through different absolute paths. Platform config rejects it
	// outside development; production always uses the deployment value.
	SkillsRoot string

	MCPTimeout        time.Duration
	MaxConcurrentRuns int
	Now               func() time.Time
	Logger            *slog.Logger
	TracerProvider    trace.TracerProvider

	// SessionService is an official ADK session.Service injection point. A
	// database implementation can be supplied without changing protocol or
	// runtime semantics. Nil selects ADK's official in-memory implementation.
	SessionService session.Service

	// modelOverride is an internal test seam. Production builds always
	// construct the OpenAI-compatible provider in NewRuntime.
	modelOverride model.LLM

	// mcpTransportOverrides is an internal test seam for protocol health
	// tests. Production always uses the official Streamable HTTP transport.
	mcpTransportOverrides map[string]mcp.Transport

	// outboundTransport is the shared, pool-tuned HTTP transport for LLM and
	// MCP traffic. NewRuntime derives it from MaxConcurrentRuns; a nil value
	// falls back to a per-call default so hand-built test configs keep working.
	outboundTransport *http.Transport
}
