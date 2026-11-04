package deployment

// DeploymentView is a read-only protocol view over a runtime-owned aggregate.
// It exposes only discovery-safe declarations: no instruction text, compiled
// schema state, raw schemas, source manifest, or internal pointers.
type DeploymentView struct {
	source *AgentDeployment
}

// View returns the read-only protocol boundary for this aggregate.
func (d *AgentDeployment) View() DeploymentView {
	return DeploymentView{source: d}
}

// Agent returns a declaration copy without loaded instruction/schema state.
func (v DeploymentView) Agent(name string) (AgentDefinition, bool) {
	definition, ok := v.source.Agent(name)
	if !ok {
		return AgentDefinition{}, false
	}
	return cloneAgentDeclaration(&definition.AgentDefinition), true
}

// Entrypoint returns a declaration copy without loaded instruction/schema state.
func (v DeploymentView) Entrypoint() (AgentDefinition, error) {
	definition, err := v.source.EntrypointDefinition()
	if err != nil {
		return AgentDefinition{}, err
	}
	return cloneAgentDeclaration(&definition.AgentDefinition), nil
}

// ExposesModelText applies the single aggregate-level visibility policy.
func (v DeploymentView) ExposesModelText(author string) bool {
	definition, ok := v.source.Agent(author)
	return ok && ExposesModelText(&definition.AgentDefinition)
}

func cloneAgentDeclaration(definition *AgentDefinition) AgentDefinition {
	clone := *definition
	clone.SubAgents = append([]AgentReference(nil), definition.SubAgents...)
	if definition.Tools.Allow != nil {
		clone.Tools.Allow = make(map[string][]string, len(definition.Tools.Allow))
		for server, tools := range definition.Tools.Allow {
			clone.Tools.Allow[server] = append([]string(nil), tools...)
		}
	}
	if definition.Skills != nil {
		skills := *definition.Skills
		clone.Skills = &skills
	}
	if definition.Temperature != nil {
		temperature := *definition.Temperature
		clone.Temperature = &temperature
	}
	return clone
}
