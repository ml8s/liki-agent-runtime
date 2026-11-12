package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/platform"
	"github.com/ml8s/agent-runtime/internal/platform/buildinfo"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HealthChecker reports readiness for one logical MCP server.
type HealthChecker struct {
	config        ClientConfig
	definition    deployment.MCPServerDefinition
	requiredTools []deployment.ToolReference
}

// NewHealthChecker returns a deployment-aware readiness checker. It verifies
// every required tool from every Agent allowlist, not merely server liveness.
func NewHealthChecker(config ClientConfig, definition deployment.MCPServerDefinition, requiredTools []deployment.ToolReference) *HealthChecker {
	return &HealthChecker{config: config, definition: definition, requiredTools: requiredTools}
}

// CheckHealth performs official server discovery and required-tool validation.
func (c *HealthChecker) CheckHealth(ctx context.Context) platform.DependencyHealth {
	name := "mcp/" + c.definition.Name
	resolved, err := ResolveServer(c.definition)
	if err != nil {
		return platform.DependencyHealth{Name: name, OK: false, Detail: HealthDetail(err)}
	}
	if err := c.check(ctx, resolved); err != nil {
		return platform.DependencyHealth{Name: name, OK: false, Detail: HealthDetail(err)}
	}
	return platform.DependencyHealth{Name: name, OK: true}
}

func (c *HealthChecker) check(ctx context.Context, server ResolvedServer) (err error) {
	if _, overridden := c.config.TransportOverrides[server.Definition.Name]; !overridden {
		if err := ValidateEndpoint(server); err != nil {
			return err
		}
	}
	healthTimeout := c.config.HealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = c.config.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "agent-runtime", Version: buildinfo.BuildID},
		nil,
	)
	var transport mcpsdk.Transport = newTransport(c.config, server)
	if override, ok := c.config.TransportOverrides[server.Definition.Name]; ok {
		transport = override
	}
	session, connectErr := client.Connect(ctx, transport, &mcpsdk.ClientSessionOptions{ProtocolVersion: ProtocolVersion()})
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
	if result.ProtocolVersion == "" || !slices.Contains(mcpsdk.SupportedProtocolVersions(), result.ProtocolVersion) {
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
	for _, tool := range c.requiredTools {
		if _, ok := advertised[tool.Name]; !ok {
			return fmt.Errorf("missing required tool %q", tool.Name)
		}
	}
	return nil
}

// HealthDetail converts common cancellation failures to bounded labels.
func HealthDetail(err error) string {
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

func tracedTransport(base *http.Transport) http.RoundTripper {
	return otelhttp.NewTransport(base)
}
