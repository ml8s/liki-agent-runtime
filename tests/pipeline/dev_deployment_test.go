package pipeline_test

import (
	"testing"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"
)

// DevelopmentAgentDeploymentRegistersComplementaryMCPTools keeps the local
// fixture wired to two generic MCP tool services.
func TestDevelopmentAgentDeploymentRegistersComplementaryMCPTools(t *testing.T) {
	artifact, err := deploymentpkg.LoadAgentDeployment("../../dev/agent-deployment/deployment.json")
	if err != nil {
		t.Fatalf("LoadAgentDeployment() error = %v", err)
	}

	servers := make(map[string]deploymentpkg.MCPServerDefinition, len(artifact.Spec.MCPServers))
	for _, server := range artifact.Spec.MCPServers {
		servers[server.Name] = server
	}
	// MCP endpoints share the AGENT_RUNTIME_MCP_<NAME> prefix so a server's
	// binding can be found by name. MCP is token-free.
	engine, ok := servers["tools-a"]
	if !ok || engine.EndpointEnv != "AGENT_RUNTIME_MCP_TOOLS_A_URL" {
		t.Fatalf("Tools A MCP declaration = %+v", engine)
	}
	counsel, ok := servers["tools-b"]
	if !ok || counsel.EndpointEnv != "AGENT_RUNTIME_MCP_TOOLS_B_URL" {
		t.Fatalf("Tools B MCP declaration = %+v", counsel)
	}

	entrypoint, err := artifact.EntrypointDefinition()
	if err != nil {
		t.Fatalf("EntrypointDefinition() error = %v", err)
	}
	if got := entrypoint.Tools.Allow["tools-a"]; len(got) == 0 {
		t.Fatal("entrypoint has no allowlisted tools-a tools")
	}
	if got := entrypoint.Tools.Allow["tools-b"]; len(got) == 0 {
		t.Fatal("entrypoint has no allowlisted tools-b tools")
	}
}
