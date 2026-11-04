package deployment

import (
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

func (l *definitionLoader) loadAgents(deployment *AgentDeployment) error {
	for index := range deployment.Spec.Agents {
		agent := &deployment.Spec.Agents[index]
		instructionRaw, err := l.readFile(agent.Instruction.Path)
		if err != nil {
			return artifactErrorWrap(CodeDeploymentArtifactReadFailed, fmt.Sprintf("agent %q instruction", agent.Name), err)
		}
		if len(instructionRaw) > maxTextArtifactBytes {
			return artifactError(CodeDeploymentArtifactTooLarge, "agent %q instruction exceeds %d bytes", agent.Name, maxTextArtifactBytes)
		}
		instruction := string(instructionRaw)
		if strings.TrimSpace(instruction) == "" {
			return artifactError(CodeDeploymentArtifactInvalid, "agent %q instruction is empty", agent.Name)
		}
		agent.InstructionText = instruction
		agent.InstructionDigest = sha256Hex([]byte(instruction))
		if agent.Output.Structured() {
			schemaRaw, err := l.readFile(agent.Output.Schema.Path)
			if err != nil {
				return artifactErrorWrap(CodeDeploymentArtifactReadFailed, fmt.Sprintf("agent %q output schema", agent.Name), err)
			}
			if len(schemaRaw) > maxSchemaArtifactBytes {
				return artifactError(CodeDeploymentArtifactTooLarge, "agent %q output schema exceeds %d bytes", agent.Name, maxSchemaArtifactBytes)
			}
			var outputSchema jsonschema.Schema
			if err := decodeStrictJSON(schemaRaw, &outputSchema); err != nil {
				return artifactErrorWrap(CodeDeploymentOutputSchemaInvalid, fmt.Sprintf("agent %q decode output schema", agent.Name), err)
			}
			resolved, err := outputSchema.Resolve(nil)
			if err != nil {
				return artifactErrorWrap(CodeDeploymentOutputSchemaInvalid, fmt.Sprintf("agent %q resolve output schema", agent.Name), err)
			}
			if err := validateOutputPointer(&outputSchema, agent.Output.TextPointer); err != nil {
				return artifactErrorWrap(CodeDeploymentOutputPointerInvalid, fmt.Sprintf("agent %q output.textPointer", agent.Name), err)
			}
			if err := validateOutputSchemaRoot(&outputSchema); err != nil {
				return artifactErrorWrap(CodeDeploymentOutputSchemaInvalid, fmt.Sprintf("agent %q output schema root", agent.Name), err)
			}
			agent.OutputSchema = &outputSchema
			agent.ResolvedOutput = resolved
			agent.SchemaDigest = sha256Hex(schemaRaw)
			var rawOutputSchema map[string]any
			if err := decodeStrictJSON(schemaRaw, &rawOutputSchema); err != nil {
				return artifactErrorWrap(CodeDeploymentOutputSchemaInvalid, fmt.Sprintf("agent %q decode raw output schema", agent.Name), err)
			}
			agent.RawOutputSchema = rawOutputSchema
		}
		agent.Digest, err = agentDigest(*agent)
		if err != nil {
			return artifactErrorWrap(CodeDeploymentIdentityInvalid, fmt.Sprintf("agent %q digest", agent.Name), err)
		}
		agent.buildToolIndex()
	}
	return nil
}

func validateOutputSchemaRoot(schema *jsonschema.Schema) error {
	if schema.Type != "object" {
		return artifactError(CodeDeploymentOutputSchemaInvalid, "root type is %q, want %q", schema.Type, "object")
	}
	return nil
}
