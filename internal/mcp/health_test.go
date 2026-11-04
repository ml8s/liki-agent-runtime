package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/platform"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type healthProbe struct {
	sawDiscover      bool
	sawPing          bool
	requestedVersion string
}

func TestCheckHealthUsesCurrentMCPDiscovery(t *testing.T) {
	probe, checker := newHealthChecker(t, &mcpsdk.ServerOptions{}, "test_tool")
	health := checkMCPHealth(t, checker)
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
	if probe.requestedVersion != ProtocolVersion() {
		t.Fatalf("requested MCP version = %q, want %q", probe.requestedVersion, ProtocolVersion())
	}
}

func TestCheckHealthAcceptsNegotiatedSupportedProtocol(t *testing.T) {
	options := &mcpsdk.ServerOptions{
		SupportedProtocolVersions: []string{"2025-11-25"},
		Capabilities: &mcpsdk.ServerCapabilities{
			Tools: &mcpsdk.ToolCapabilities{},
		},
	}
	_, checker := newHealthChecker(t, options, "test_tool")
	if health := checkMCPHealth(t, checker); !health.OK {
		t.Fatalf("CheckHealth() rejected a supported negotiated protocol: %+v", health)
	}
}

func TestCheckHealthRejectsMissingToolCapability(t *testing.T) {
	options := &mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{}}
	_, checker := newHealthChecker(t, options, "")
	if health := checkMCPHealth(t, checker); health.OK {
		t.Fatal("CheckHealth() accepted a server without tool capability")
	}
}

func TestCheckHealthRejectsMissingAllowedTool(t *testing.T) {
	_, checker := newHealthChecker(t, &mcpsdk.ServerOptions{}, "available_tool")
	checker.requiredTools = []deployment.ToolReference{{Server: "test", Name: "missing_tool"}}
	if health := checkMCPHealth(t, checker); health.OK {
		t.Fatal("CheckHealth() accepted a server missing an allowlisted tool")
	}
}

func TestCheckHealthValidatesEveryAgentAllowlist(t *testing.T) {
	_, checker := newHealthChecker(t, &mcpsdk.ServerOptions{}, "available_tool")
	checker.requiredTools = []deployment.ToolReference{{Server: "test", Name: "missing_tool"}}
	health := checkMCPHealth(t, checker)
	if health.OK {
		t.Fatal("CheckHealth() accepted a missing required tool")
	}
	if !strings.Contains(health.Detail, `missing required tool "missing_tool"`) {
		t.Fatalf("health detail = %q, want missing tool", health.Detail)
	}
}

func newHealthChecker(
	t *testing.T,
	options *mcpsdk.ServerOptions,
	toolName string,
) (*healthProbe, *HealthChecker) {
	t.Helper()
	t.Setenv("TEST_MCP_ENDPOINT", "http://test.invalid/mcp")
	probe := &healthProbe{}
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "test-mcp", Version: "test"},
		options,
	)
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, request mcpsdk.Request) (mcpsdk.Result, error) {
			switch method {
			case "server/discover":
				probe.sawDiscover = true
				discover, ok := request.(*mcpsdk.ServerRequest[*mcpsdk.DiscoverParams])
				if !ok {
					t.Errorf("discover request has unexpected type %T", request)
					break
				}
				if version, _ := discover.Params.GetMeta()[mcpsdk.MetaKeyProtocolVersion].(string); version != "" {
					probe.requestedVersion = version
				}
			case "ping":
				probe.sawPing = true
			}
			return next(ctx, method, request)
		}
	})
	if toolName != "" {
		server.AddTool(&mcpsdk.Tool{
			Name:        toolName,
			Description: "test deterministic chart",
			InputSchema: &jsonschema.Schema{Type: "object"},
		}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{}, nil
		})
	}
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	config := ClientConfig{
		Timeout:       5 * time.Second,
		HealthTimeout: 5 * time.Second,
		TransportOverrides: TransportOverrides{
			"test": clientTransport,
		},
	}
	definition := deployment.MCPServerDefinition{Name: "test", EndpointEnv: "TEST_MCP_ENDPOINT"}
	var required []deployment.ToolReference
	if toolName != "" {
		required = []deployment.ToolReference{{Server: "test", Name: toolName}}
	}
	return probe, NewHealthChecker(config, definition, required)
}

func checkMCPHealth(t *testing.T, checker *HealthChecker) platform.DependencyHealth {
	t.Helper()
	return checker.CheckHealth(context.Background())
}
