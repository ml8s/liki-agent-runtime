package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	deploymentpkg "github.com/ml8s/liki-agent-runtime/internal/deployment"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ml8s/liki-agent-runtime/internal/platform"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type healthProbe struct {
	sawDiscover      bool
	sawPing          bool
	requestedVersion string
}

func TestCheckHealthUsesCurrentMCPDiscovery(t *testing.T) {
	probe, runtime := newHealthTestRuntime(t, &mcp.ServerOptions{}, "test_tool")
	health := checkMCPHealth(t, runtime)
	if !health.OK {
		t.Fatalf("CheckHealth() = %+v, want ready", health)
	}
	if health.Name != "mcp/test" {
		t.Fatalf("dependency name = %q", health.Name)
	}
	if !probe.sawDiscover {
		t.Fatal("server did not receive server/discover")
	}
	if probe.sawPing {
		t.Error("health probe sent retired ping RPC")
	}
	if probe.requestedVersion != mcpProtocolVersion() {
		t.Fatalf("requested MCP version = %q, want %q", probe.requestedVersion, mcpProtocolVersion())
	}
}

func TestMCPHealthChecksExposeEachLogicalServer(t *testing.T) {
	_, runtime := newHealthTestRuntime(t, &mcp.ServerOptions{}, "test_tool")
	checkers := runtime.MCPHealthChecks()
	if len(checkers) != 1 {
		t.Fatalf("MCP health checkers = %d, want 1", len(checkers))
	}
	if got := checkers[0].CheckHealth(context.Background()).Name; got != "mcp/test" {
		t.Fatalf("MCP health name = %q, want mcp/test", got)
	}
}

func TestCheckHealthAcceptsNegotiatedSupportedProtocol(t *testing.T) {
	// A compliant server may negotiate an older protocol version it supports;
	// readiness must accept it instead of demanding the newest revision.
	options := &mcp.ServerOptions{
		SupportedProtocolVersions: []string{"2025-11-25"},
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{},
		},
	}
	_, runtime := newHealthTestRuntime(t, options, "test_tool")
	health := checkMCPHealth(t, runtime)
	if !health.OK {
		t.Fatalf("CheckHealth() rejected a supported negotiated protocol: %+v", health)
	}
}

func TestCheckHealthRejectsMissingToolCapability(t *testing.T) {
	options := &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}}
	_, runtime := newHealthTestRuntime(t, options, "")
	health := checkMCPHealth(t, runtime)
	if health.OK {
		t.Fatal("CheckHealth() accepted a server without tool capability")
	}
}

func newHealthTestRuntime(t *testing.T, options *mcp.ServerOptions, toolName string) (*healthProbe, *Runtime) {
	t.Helper()
	t.Setenv("TEST_MCP_ENDPOINT", "http://test.invalid/mcp")

	probe := &healthProbe{}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-engine", Version: "test"},
		options,
	)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			switch method {
			case "server/discover":
				probe.sawDiscover = true
				discover, ok := request.(*mcp.ServerRequest[*mcp.DiscoverParams])
				if !ok {
					t.Errorf("discover request has unexpected type %T", request)
					break
				}
				if version, _ := discover.Params.GetMeta()[mcp.MetaKeyProtocolVersion].(string); version != "" {
					probe.requestedVersion = version
				}
			case "ping":
				probe.sawPing = true
			}
			return next(ctx, method, request)
		}
	})
	if toolName != "" {
		server.AddTool(&mcp.Tool{
			Name:        toolName,
			Description: "test deterministic chart",
			InputSchema: &jsonschema.Schema{Type: "object"},
		}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	}
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	return probe, &Runtime{config: Config{
		MCPTimeout: 5 * time.Second,
		mcpTransportOverrides: map[string]mcp.Transport{
			"test": clientTransport,
		},
		AgentDeployment: &deploymentpkg.AgentDeployment{Spec: deploymentpkg.DeploymentSpec{
			MCPServers: []deploymentpkg.MCPServerDefinition{{Name: "test", EndpointEnv: "TEST_MCP_ENDPOINT"}},
			Agents: resolvedAgentDefinitions([]deploymentpkg.AgentDefinition{{
				Name:  "test-agent",
				Tools: deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {toolName}}},
			}}),
		}},
	}}
}

func checkMCPHealth(t *testing.T, runtime *Runtime) platform.DependencyHealth {
	t.Helper()
	checkers := runtime.MCPHealthChecks()
	if len(checkers) != 1 {
		t.Fatalf("MCP health checkers = %d, want 1", len(checkers))
	}
	return checkers[0].CheckHealth(context.Background())
}

func TestCheckHealthRejectsMissingAllowedTool(t *testing.T) {
	_, runtime := newHealthTestRuntime(t, &mcp.ServerOptions{}, "available_tool")
	runtime.config.AgentDeployment.Spec.Agents[0].Tools = deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {"missing_tool"}}}
	health := checkMCPHealth(t, runtime)
	if health.OK {
		t.Fatal("CheckHealth() accepted a server missing an allowlisted tool")
	}
}

func TestCheckHealthValidatesEveryAgentAllowlist(t *testing.T) {
	_, runtime := newHealthTestRuntime(t, &mcp.ServerOptions{}, "available_tool")
	runtime.config.AgentDeployment = &deploymentpkg.AgentDeployment{Spec: deploymentpkg.DeploymentSpec{
		MCPServers: []deploymentpkg.MCPServerDefinition{{Name: "test", EndpointEnv: "TEST_MCP_ENDPOINT"}},
		Agents: resolvedAgentDefinitions([]deploymentpkg.AgentDefinition{
			{Name: "entrypoint", Tools: deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {"available_tool"}}}},
			{Name: "worker", Tools: deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {"missing_tool"}}}},
		}),
	}}
	health := checkMCPHealth(t, runtime)
	if health.OK {
		t.Fatal("CheckHealth() validated only the entrypoint allowlist")
	}
	if !strings.Contains(health.Detail, `missing required tool "missing_tool"`) {
		t.Fatalf("health detail = %q, want missing tool", health.Detail)
	}
}
