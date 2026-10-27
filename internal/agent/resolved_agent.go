package agent

import (
	"fmt"

	agentdeployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

	"google.golang.org/genai"
)

// RuntimeAgentDefinition is the provider-facing view used to compile one
// resolved Agent into ADK. JSON Schema provider mappings are intentionally not
// part of the deployment aggregate.
type RuntimeAgentDefinition struct {
	Definition        agentdeployment.ResolvedAgentDefinition
	GenaiOutputSchema *genai.Schema
	RawOutputSchema   map[string]any
}

// NewRuntimeAgentDefinition compiles the resolved schema into the provider view
// required by ADK. Plain-text Agents have no output-schema mappings.
func NewRuntimeAgentDefinition(
	resolved agentdeployment.ResolvedAgentDefinition,
) (RuntimeAgentDefinition, error) {
	if !resolved.Output.Structured() {
		return RuntimeAgentDefinition{Definition: resolved}, nil
	}
	if resolved.OutputSchema == nil {
		return RuntimeAgentDefinition{}, fmt.Errorf(
			"agent %q has structured output without a resolved schema",
			resolved.Name,
		)
	}
	genaiSchema, err := ConvertJSONSchemaToGenai(resolved.OutputSchema)
	if err != nil {
		return RuntimeAgentDefinition{}, fmt.Errorf(
			"agent %q output schema: %w",
			resolved.Name,
			err,
		)
	}
	if genaiSchema.Type != genai.TypeObject {
		return RuntimeAgentDefinition{}, fmt.Errorf(
			"agent %q output schema root must be an object",
			resolved.Name,
		)
	}
	return RuntimeAgentDefinition{
		Definition:        resolved,
		GenaiOutputSchema: genaiSchema,
		RawOutputSchema:   resolved.RawOutputSchema,
	}, nil
}

func resolvedAgentDefinitions(definitions []agentdeployment.AgentDefinition) []agentdeployment.ResolvedAgentDefinition {
	resolved := make([]agentdeployment.ResolvedAgentDefinition, 0, len(definitions))
	for index := range definitions {
		definition := &definitions[index]
		resolved = append(resolved, agentdeployment.ResolvedAgentDefinition{AgentDefinition: *definition})
	}
	return resolved
}
