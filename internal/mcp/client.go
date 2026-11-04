// Package mcp owns client-side MCP transport, discovery caching, warmup, and
// deployment-aware readiness. The official SDK owns protocol behavior.
package mcp

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/domain"
	"github.com/ml8s/agent-runtime/internal/execution"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
)

// ClientConfig owns the external dependencies and budgets shared by toolsets,
// warmup, and readiness checks.
type ClientConfig struct {
	Timeout           time.Duration
	HealthTimeout     time.Duration
	Now               func() time.Time
	Metrics           execution.Metrics
	OutboundTransport *http.Transport
	// TransportOverrides is a test seam. Production uses standard Streamable HTTP.
	TransportOverrides TransportOverrides
}

// ResolvedServer binds a logical declaration to its environment-owned endpoint.
type ResolvedServer struct {
	Definition deployment.MCPServerDefinition
	Endpoint   string
}

// TransportOverrides is a test-only transport seam keyed by logical server.
type TransportOverrides map[string]mcpsdk.Transport

// ProtocolVersion returns the latest stateless MCP revision supported by the
// official SDK.
func ProtocolVersion() string {
	return mcpsdk.SupportedProtocolVersions()[0]
}

// ResolveServer reads the deployment-owned endpoint environment reference.
func ResolveServer(definition deployment.MCPServerDefinition) (ResolvedServer, error) {
	endpoint, ok := os.LookupEnv(definition.EndpointEnv)
	if !ok || strings.TrimSpace(endpoint) == "" {
		return ResolvedServer{}, domain.NewError(
			domain.CodeMCPEndpointEnvMissing,
			fmt.Sprintf("MCP endpoint environment %q is required for server %q", definition.EndpointEnv, definition.Name),
			nil,
		)
	}
	return ResolvedServer{Definition: definition, Endpoint: strings.TrimSpace(endpoint)}, nil
}

// ValidateEndpoint fails closed before connecting to a production transport.
func ValidateEndpoint(server ResolvedServer) error {
	endpointURL, err := url.Parse(server.Endpoint)
	if err != nil ||
		endpointURL.Scheme != "http" && endpointURL.Scheme != "https" ||
		endpointURL.Host == "" ||
		endpointURL.User != nil {
		return domain.NewError(
			domain.CodeMCPEndpointInvalid,
			fmt.Sprintf(
				"MCP endpoint environment %q must be an absolute HTTP(S) URL without credentials",
				server.Definition.EndpointEnv,
			),
			nil,
		)
	}
	return nil
}

// NewToolsets creates one cached toolset per declared logical MCP server.
func NewToolsets(config ClientConfig, definitions []deployment.MCPServerDefinition) ([]tool.Toolset, error) {
	toolsets := make([]tool.Toolset, 0, len(definitions))
	for _, definition := range definitions {
		resolved, err := ResolveServer(definition)
		if err != nil {
			return nil, err
		}
		if _, overridden := config.TransportOverrides[definition.Name]; !overridden {
			if err := ValidateEndpoint(resolved); err != nil {
				return nil, err
			}
		}
		var transport mcpsdk.Transport = newTransport(config, resolved)
		if override, ok := config.TransportOverrides[definition.Name]; ok {
			transport = override
		}
		toolset, err := mcptoolset.New(mcptoolset.Config{Transport: transport})
		if err != nil {
			return nil, domain.NewError(domain.CodeMCPToolsUnavailable, fmt.Sprintf("create MCP toolset %q", definition.Name), err)
		}
		toolsets = append(toolsets, newCachedToolset(definition.Name, toolset, mcpCatalogTTL, config.Now, config.Metrics))
	}
	return toolsets, nil
}

func newTransport(config ClientConfig, server ResolvedServer) *mcpsdk.StreamableClientTransport {
	base := config.OutboundTransport
	if base == nil {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			defaultTransport = &http.Transport{}
		}
		base = defaultTransport
	}
	httpClient := &http.Client{Timeout: config.Timeout, Transport: tracedTransport(base)}
	return &mcpsdk.StreamableClientTransport{
		Endpoint:             server.Endpoint,
		HTTPClient:           httpClient,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
}
