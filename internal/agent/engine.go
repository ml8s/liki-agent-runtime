package agent

import (
	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"
	mcppkg "github.com/ml8s/agent-runtime/internal/mcp"
	"github.com/ml8s/agent-runtime/internal/platform"
)

// MCPHealthChecks returns one readiness checker per declared logical MCP
// server. Composite readiness cannot hide an individual dependency failure.
func (r *Runtime) MCPHealthChecks() []platform.HealthChecker {
	definitions := r.deploymentMCPServers()
	checkers := make([]platform.HealthChecker, 0, len(definitions))
	for _, definition := range definitions {
		checkers = append(checkers, mcppkg.NewHealthChecker(
			newMCPClientConfig(r.config),
			definition,
			deploymentToolReferences(r.definition, definition.Name),
		))
	}
	return checkers
}

func (r *Runtime) deploymentMCPServers() []agentdeployment.MCPServerDefinition {
	if r.definition == nil {
		return nil
	}
	return r.definition.Spec.MCPServers
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
