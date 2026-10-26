package agent

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/genai"
)

// ResolvedAgentDefinition is an AgentDeclaration after the deployment loader
// has loaded external artifacts and computed immutable identity. It contains
// runtime-neutral resolved state; provider mappings belong to the runtime view.
type ResolvedAgentDefinition struct {
	AgentDefinition

	InstructionText   string               `json:"-"`
	InstructionDigest string               `json:"-"`
	SchemaDigest      string               `json:"-"`
	OutputSchema      *jsonschema.Schema   `json:"-"`
	ResolvedOutput    *jsonschema.Resolved `json:"-"`
	Digest            string               `json:"-"`
	// RawOutputSchema is the loaded external schema in its neutral resolved
	// representation. Provider mappings are derived in RuntimeAgentDefinition.
	RawOutputSchema map[string]any `json:"-"`
}

// RuntimeAgentDefinition is the provider-facing view used to compile one
// resolved Agent into ADK. JSON Schema provider mappings are intentionally not
// part of the deployment aggregate.
type RuntimeAgentDefinition struct {
	Definition        ResolvedAgentDefinition
	GenaiOutputSchema *genai.Schema
	RawOutputSchema   map[string]any
}

// NewRuntimeAgentDefinition compiles the resolved schema into the provider view
// required by ADK. Plain-text Agents have no output-schema mappings.
func NewRuntimeAgentDefinition(
	resolved ResolvedAgentDefinition,
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
	genaiSchema, err := convertJSONSchemaToGenai(resolved.OutputSchema)
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

func resolvedAgentDefinitions(definitions []AgentDefinition) []ResolvedAgentDefinition {
	resolved := make([]ResolvedAgentDefinition, 0, len(definitions))
	for index := range definitions {
		definition := &definitions[index]
		resolved = append(resolved, ResolvedAgentDefinition{AgentDefinition: *definition})
	}
	return resolved
}
