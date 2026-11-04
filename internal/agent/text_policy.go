package agent

import (
	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"
)

// deploymentTextPolicy adapts the immutable aggregate to the runtime's
// text-visibility port. It delegates to the single deployment policy function.
type deploymentTextPolicy struct {
	definition *agentdeployment.AgentDeployment
}

func (p deploymentTextPolicy) ExposesModelText(author string) bool {
	definition, ok := p.definition.Agent(author)
	if !ok {
		return false
	}
	return agentdeployment.ExposesModelText(&definition.AgentDefinition)
}
