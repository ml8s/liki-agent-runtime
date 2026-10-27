package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"slices"

	agentdeployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

	"github.com/ml8s/liki-agent-runtime/internal/platform"
	"github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/adk/v2/auth"
)

// mcpProtocolVersion returns the latest stateless MCP revision supported by
// the official SDK. Server discovery is the health mechanism; ping and legacy
// HTTP health endpoints are deliberately not used.
func mcpProtocolVersion() string {
	return mcp.SupportedProtocolVersions()[0]
}

// MCPHealthChecks returns one readiness checker per declared logical MCP
// server. Composite readiness cannot hide an individual dependency failure.
// The official MCP SDK owns protocol behavior; the runtime owns
// deployment-aware readiness.
func (r *Runtime) MCPHealthChecks() []platform.HealthChecker {
	definitions := r.deploymentMCPServers()
	checkers := make([]platform.HealthChecker, 0, len(definitions))
	for _, definition := range definitions {
		checkers = append(checkers, &mcpServerHealthChecker{
			runtime:    r,
			definition: definition,
		})
	}
	return checkers
}

type mcpServerHealthChecker struct {
	runtime    *Runtime
	definition agentdeployment.MCPServerDefinition
}

func (c *mcpServerHealthChecker) CheckHealth(ctx context.Context) platform.DependencyHealth {
	name := "mcp/" + c.definition.Name
	resolved, err := resolveMCPServer(c.definition)
	if err != nil {
		return platform.DependencyHealth{Name: name, OK: false, Detail: healthDetail(err)}
	}
	if err := c.runtime.checkMCPDependency(ctx, resolved); err != nil {
		return platform.DependencyHealth{Name: name, OK: false, Detail: healthDetail(err)}
	}
	return platform.DependencyHealth{Name: name, OK: true}
}

func checkMCPDependencyHealth(
	ctx context.Context,
	config Config,
	server resolvedMCPServer,
	requiredTools []agentdeployment.ToolReference,
) (err error) {
	if _, overridden := config.mcpTransportOverrides[server.definition.Name]; !overridden {
		if err := validateMCPEndpoint(server); err != nil {
			return err
		}
	}
	// Hand-built test configs may leave the split health budget unset; the
	// runtime constructor defaults it to the tool-call budget.
	healthTimeout := config.MCPHealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = config.MCPTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	client := mcp.NewClient(
		&mcp.Implementation{Name: "liki-agent-runtime", Version: buildinfo.Version},
		nil,
	)
	var transport mcp.Transport = newMCPTransport(config, server)
	if override, ok := config.mcpTransportOverrides[server.definition.Name]; ok {
		transport = override
	}
	session, connectErr := client.Connect(
		ctx,
		transport,
		&mcp.ClientSessionOptions{ProtocolVersion: mcpProtocolVersion()},
	)
	if connectErr != nil {
		return connectErr
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close MCP session after health check: %w", closeErr)
		}
	}()

	result := session.InitializeResult()
	if result == nil {
		return errors.New("discovery returned no result")
	}
	if result.ProtocolVersion == "" || !slices.Contains(mcp.SupportedProtocolVersions(), result.ProtocolVersion) {
		return fmt.Errorf("negotiated unsupported MCP protocol %q", result.ProtocolVersion)
	}
	if result.ServerInfo == nil || result.ServerInfo.Name == "" {
		return errors.New("discovery omitted server identity")
	}
	if result.Capabilities == nil || result.Capabilities.Tools == nil {
		return errors.New("server does not advertise tool capability")
	}
	listed, listErr := session.ListTools(ctx, nil)
	if listErr != nil {
		return listErr
	}
	advertised := make(map[string]struct{}, len(listed.Tools))
	for _, tool := range listed.Tools {
		advertised[tool.Name] = struct{}{}
	}
	for _, tool := range requiredTools {
		if _, ok := advertised[tool.Name]; !ok {
			return fmt.Errorf("missing required tool %q", tool.Name)
		}
	}
	return nil
}

func (r *Runtime) deploymentMCPServers() []agentdeployment.MCPServerDefinition {
	if r.config.AgentDeployment == nil {
		return nil
	}
	return r.config.AgentDeployment.Spec.MCPServers
}

func deploymentToolReferences(deployment *agentdeployment.AgentDeployment, server string) []agentdeployment.ToolReference {
	if deployment == nil {
		return nil
	}
	references := make([]agentdeployment.ToolReference, 0)
	for _, definition := range deployment.Spec.Agents {
		for _, reference := range definition.Tools.References() {
			if reference.Server == server {
				references = append(references, reference)
			}
		}
	}
	return references
}

func (r *Runtime) checkMCPDependency(
	ctx context.Context,
	server resolvedMCPServer,
) error {
	return checkMCPDependencyHealth(
		ctx,
		r.config,
		server,
		deploymentToolReferences(r.config.AgentDeployment, server.definition.Name),
	)
}

func healthDetail(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return err.Error()
}

func newMCPTransport(config Config, server resolvedMCPServer) *mcp.StreamableClientTransport {
	base := config.outboundTransport
	if base == nil {
		base = newOutboundTransport(config.MaxConcurrentRuns)
	}
	traced := otelhttp.NewTransport(base)
	var transport http.RoundTripper = traced
	if server.Token != "" {
		transport = &auth.Transport{
			Provider: auth.StaticToken(server.Token),
			Base:     traced,
		}
	}
	httpClient := &http.Client{Timeout: config.MCPTimeout, Transport: transport}
	return &mcp.StreamableClientTransport{
		Endpoint:   server.Endpoint,
		HTTPClient: httpClient,
		// A negative value disables SDK reconnect retries. Calls fail fast;
		// the run deadline and ADK's retry policy own recovery.
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
}

// newOutboundTransport returns the shared HTTP transport for outbound LLM and
// MCP traffic. It clones the standard transport so proxy, HTTP/2, and timeout
// defaults are preserved, then sizes the per-host idle pool from the run
// concurrency bound: http.DefaultTransport keeps only two idle connections
// per host, which would repeat TCP and TLS handshakes under concurrent runs.
// Doubling the bound leaves headroom for parallel sub-agent model calls
// inside one run.
func newOutboundTransport(maxConcurrentRuns int) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	tuned := base.Clone()
	// ADK executes the function calls of one model response concurrently
	// (platform.RunTasks), so concurrent runs multiply their outbound fan-out
	// by the parallel-tool count. Size the idle pool for that fan-out so a
	// burst of parallel tool calls reuses warm connections instead of paying
	// a TCP+TLS handshake per call.
	perHost := max(64, min(512, 8*maxConcurrentRuns))
	tuned.MaxIdleConns = max(tuned.MaxIdleConns, perHost)
	tuned.MaxIdleConnsPerHost = perHost
	// Cache TLS session tickets so a fresh connection after an idle gap
	// resumes instead of paying a full handshake.
	if tuned.TLSClientConfig == nil {
		tuned.TLSClientConfig = &tls.Config{}
	}
	tuned.TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(0)
	return tuned
}
