package agent

import (
	agentdeployment "github.com/ml8s/liki-agent-runtime/internal/deployment"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"google.golang.org/adk/v2/model"
)

// modelRegistry is immutable after construction. AgentDeployment graph validation
// guarantees every Agent is reachable from the entrypoint, so construction can
// eagerly build each distinct model client once and runtime event flow only
// performs read-only lookups.
type modelRegistry struct {
	models map[string]model.LLM
}

func newModelRegistry(config Config, deployment *agentdeployment.AgentDeployment) (modelRegistry, error) {
	return newModelRegistryWithBuilder(newModelBuilder(config), deploymentModelNames(deployment, config.Model))
}

func newModelRegistryWithBuilder(
	build func(name string) (model.LLM, error),
	names []string,
) (modelRegistry, error) {
	models := make(map[string]model.LLM, len(names))
	for _, name := range names {
		if _, built := models[name]; built {
			continue
		}
		built, err := build(name)
		if err != nil {
			return modelRegistry{}, err
		}
		models[name] = built
	}
	return modelRegistry{models: models}, nil
}

func deploymentModelNames(deployment *agentdeployment.AgentDeployment, defaultModel string) []string {
	names := make([]string, 0, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		names = append(names, deployment.Spec.Agents[index].EffectiveModel(defaultModel))
	}
	return names
}

func (r modelRegistry) resolve(name string) (model.LLM, error) {
	if built, ok := r.models[name]; ok {
		return built, nil
	}
	return nil, domain.NewError(
		domain.CodeLLMModelMissing,
		"model is not registered for the deployment",
		domain.ErrInvalidInput,
	)
}
