package agent

import (
	"fmt"

	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"

	"github.com/ml8s/agent-runtime/internal/domain"

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
// required by ADK. Plain-text Agents have no output-schema mappings. When
// nativeStructuredOutput is true the raw JSON Schema callback is skipped; the
// provider model translates genai.Schema natively.
func NewRuntimeAgentDefinition(
	resolved agentdeployment.ResolvedAgentDefinition,
	nativeStructuredOutput bool,
) (RuntimeAgentDefinition, error) {
	if !resolved.Output.Structured() {
		return RuntimeAgentDefinition{Definition: resolved}, nil
	}
	if resolved.OutputSchema == nil {
		return RuntimeAgentDefinition{}, domain.NewError(domain.CodeStructuredSchemaInvalid,
			fmt.Sprintf("agent %q has structured output without a resolved schema", resolved.Name), nil,
		)
	}
	genaiSchema, err := ConvertJSONSchemaToGenai(resolved.OutputSchema)
	if err != nil {
		return RuntimeAgentDefinition{}, domain.NewError(domain.CodeStructuredSchemaInvalid,
			fmt.Sprintf("agent %q output schema", resolved.Name), err,
		)
	}
	if genaiSchema.Type != genai.TypeObject {
		return RuntimeAgentDefinition{}, domain.NewError(domain.CodeStructuredSchemaInvalid,
			fmt.Sprintf("agent %q output schema root must be an object", resolved.Name), nil,
		)
	}
	result := RuntimeAgentDefinition{
		Definition:        resolved,
		GenaiOutputSchema: genaiSchema,
	}
	if !nativeStructuredOutput {
		result.RawOutputSchema = resolved.RawOutputSchema
	}
	return result, nil
}
