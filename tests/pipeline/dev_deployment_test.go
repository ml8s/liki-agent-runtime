package pipeline_test

import (
	"testing"

	agentruntime "github.com/ml8s/liki-agents/internal/agent"
)

// DevelopmentAgentDeploymentRegistersComplementaryMCPTools keeps the local
// fixture wired to both deterministic Engine facts and Counsel judgments.
func TestDevelopmentAgentDeploymentRegistersComplementaryMCPTools(t *testing.T) {
	deployment, err := agentruntime.LoadAgentDeployment("../../dev/agent-deployment/deployment.json")
	if err != nil {
		t.Fatalf("LoadAgentDeployment() error = %v", err)
	}

	servers := make(map[string]agentruntime.MCPServerDefinition, len(deployment.Spec.MCPServers))
	for _, server := range deployment.Spec.MCPServers {
		servers[server.Name] = server
	}
	// Endpoint and token variables share the LIKI_MCP_<NAME> prefix so a
	// server's binding can be found by name.
	engine, ok := servers["engine"]
	if !ok || engine.EndpointEnv != "LIKI_MCP_ENGINE_URL" || engine.TokenEnv != "LIKI_MCP_ENGINE_TOKEN" {
		t.Fatalf("Engine MCP declaration = %+v", engine)
	}
	counsel, ok := servers["counsel"]
	if !ok || counsel.EndpointEnv != "LIKI_MCP_COUNSEL_URL" || counsel.TokenEnv != "LIKI_MCP_COUNSEL_TOKEN" {
		t.Fatalf("Counsel MCP declaration = %+v", counsel)
	}

	entrypoint, err := deployment.EntrypointDefinition()
	if err != nil {
		t.Fatalf("EntrypointDefinition() error = %v", err)
	}
	if got := entrypoint.Tools.Allow["engine"]; len(got) == 0 {
		t.Fatal("entrypoint has no allowlisted Engine tools")
	}
	if got := entrypoint.Tools.Allow["counsel"]; len(got) == 0 {
		t.Fatal("entrypoint has no allowlisted Counsel tools")
	}
}
