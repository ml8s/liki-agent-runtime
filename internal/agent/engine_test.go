package agent

import (
	"context"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"
	mcppkg "github.com/ml8s/agent-runtime/internal/mcp"
	testagentpkg "github.com/ml8s/agent-runtime/internal/testagent"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPHealthChecksExposeEachLogicalServer verifies the runtime adapter turns
// every declared logical dependency into one health checker; detailed MCP
// protocol behavior is covered by internal/mcp.
func TestMCPHealthChecksExposeEachLogicalServer(t *testing.T) {
	t.Setenv("TEST_MCP_ENDPOINT", "http://test.invalid/mcp")
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	server := mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: "test-mcp", Version: "test"},
		&mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{
			Tools: &mcpsdk.ToolCapabilities{},
		}},
	)
	server.AddTool(&mcpsdk.Tool{
		Name:        "test_tool",
		InputSchema: &jsonschema.Schema{Type: "object"},
	}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{}, nil
	})
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	agentDeployment := &deploymentpkg.AgentDeployment{Spec: deploymentpkg.DeploymentSpec{
		MCPServers: []deploymentpkg.MCPServerDefinition{{Name: "test", EndpointEnv: "TEST_MCP_ENDPOINT"}},
		Agents: testagentpkg.ResolvedAgents([]deploymentpkg.AgentDefinition{{
			Name:  "test-agent",
			Tools: deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {"test_tool"}}},
		}}),
	}}
	runtime := &Runtime{
		definition: agentDeployment,
		config: Config{
			MCPTimeout:            5 * time.Second,
			mcpTransportOverrides: mcppkg.TransportOverrides{"test": clientTransport},
			AgentDeployment:       agentDeployment,
		},
	}

	checkers := runtime.MCPHealthChecks()
	if len(checkers) != 1 {
		t.Fatalf("MCP health checkers = %d, want 1", len(checkers))
	}
	health := checkers[0].CheckHealth(context.Background())
	if !health.OK {
		t.Fatalf("CheckHealth() = %+v, want ready", health)
	}
	if health.Name != "mcp/test" {
		t.Fatalf("dependency name = %q", health.Name)
	}
}
