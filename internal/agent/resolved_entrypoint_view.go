package agent

import (
	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/execution"
)

// ResolvedEntrypointView is the read-only boundary for structured-output
// protocol mapping. It intentionally exposes no instruction, tool allowlist,
// delegation graph, mutable schema pointer, or raw schema map.
type ResolvedEntrypointView interface {
	Name() string
	ParseStructuredOutput(value any) (execution.OutputResult, error)
}

type resolvedEntrypointView struct {
	definition *agentdeployment.ResolvedAgentDefinition
}

func newResolvedEntrypointView(definition *agentdeployment.ResolvedAgentDefinition) ResolvedEntrypointView {
	return resolvedEntrypointView{definition: definition}
}

func (v resolvedEntrypointView) Name() string {
	return v.definition.Name
}

func (v resolvedEntrypointView) ParseStructuredOutput(value any) (execution.OutputResult, error) {
	return execution.ParseStructuredOutput(value, v.definition)
}
