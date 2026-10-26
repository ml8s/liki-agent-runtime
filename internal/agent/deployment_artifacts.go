package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/genai"
)

func (l *definitionLoader) loadAgents(deployment *AgentDeployment) error {
	for index := range deployment.Spec.Agents {
		agent := &deployment.Spec.Agents[index]
		instructionRaw, err := l.readFile(agent.Instruction.Path)
		if err != nil {
			return fmt.Errorf("agent %q instruction: %w", agent.Name, err)
		}
		if len(instructionRaw) > maxTextArtifactBytes {
			return fmt.Errorf("agent %q instruction exceeds %d bytes", agent.Name, maxTextArtifactBytes)
		}
		instruction := string(instructionRaw)
		if strings.TrimSpace(instruction) == "" {
			return fmt.Errorf("agent %q instruction is empty", agent.Name)
		}
		agent.InstructionText = instruction
		agent.InstructionDigest = sha256Hex([]byte(instruction))
		if agent.Output.Structured() {
			schemaRaw, err := l.readFile(agent.Output.Schema.Path)
			if err != nil {
				return fmt.Errorf("agent %q output schema: %w", agent.Name, err)
			}
			if len(schemaRaw) > maxSchemaArtifactBytes {
				return fmt.Errorf("agent %q output schema exceeds %d bytes", agent.Name, maxSchemaArtifactBytes)
			}
			var outputSchema jsonschema.Schema
			if err := decodeStrictJSON(schemaRaw, &outputSchema); err != nil {
				return fmt.Errorf("agent %q decode output schema: %w", agent.Name, err)
			}
			resolved, err := outputSchema.Resolve(nil)
			if err != nil {
				return fmt.Errorf("agent %q resolve output schema: %w", agent.Name, err)
			}
			if err := validateOutputPointer(&outputSchema, agent.Output.TextPointer); err != nil {
				return fmt.Errorf("agent %q output.textPointer: %w", agent.Name, err)
			}
			if err := validateOutputSchemaRoot(&outputSchema); err != nil {
				return fmt.Errorf("agent %q output schema root must be an object", agent.Name)
			}
			agent.OutputSchema = &outputSchema
			agent.ResolvedOutput = resolved
			agent.SchemaDigest = sha256Hex(schemaRaw)
			var rawOutputSchema map[string]any
			if err := decodeStrictJSON(schemaRaw, &rawOutputSchema); err != nil {
				return fmt.Errorf("agent %q decode raw output schema: %w", agent.Name, err)
			}
			agent.RawOutputSchema = rawOutputSchema
		}
		agent.Digest, err = agentDigest(*agent)
		if err != nil {
			return fmt.Errorf("agent %q digest: %w", agent.Name, err)
		}
	}
	return nil
}

func convertJSONSchemaToGenai(schema *jsonschema.Schema) (*genai.Schema, error) {
	if schema == nil {
		return nil, errors.New("schema is required")
	}
	if schema.Ref != "" || len(schema.AllOf) != 0 || len(schema.OneOf) != 0 || len(schema.AnyOf) != 0 || schema.Not != nil {
		return nil, errors.New("unsupported schema composition")
	}
	if len(schema.Types) != 0 {
		return nil, errors.New("schema type arrays are unsupported")
	}
	result := &genai.Schema{
		Description: schema.Description,
		Enum:        make([]string, 0, len(schema.Enum)),
		Format:      schema.Format,
		Title:       schema.Title,
	}
	for _, value := range schema.Enum {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode enum value: %w", err)
		}
		var text string
		if err := json.Unmarshal(encoded, &text); err != nil {
			return nil, errors.New("only string enums are supported")
		}
		result.Enum = append(result.Enum, text)
	}
	if schema.Type != "" {
		genaiType, err := genaiType(schema.Type)
		if err != nil {
			return nil, err
		}
		result.Type = genaiType
	}
	copyInt64 := func(value *int) (*int64, error) {
		if value == nil {
			return nil, nil
		}
		converted := int64(*value)
		return &converted, nil
	}
	copyFloat64 := func(value *float64) (*float64, error) {
		if value == nil {
			return nil, nil
		}
		converted := *value
		return &converted, nil
	}
	var err error
	if result.MinItems, err = copyInt64(schema.MinItems); err != nil {
		return nil, err
	}
	if result.MaxItems, err = copyInt64(schema.MaxItems); err != nil {
		return nil, err
	}
	if result.MinLength, err = copyInt64(schema.MinLength); err != nil {
		return nil, err
	}
	if result.MaxLength, err = copyInt64(schema.MaxLength); err != nil {
		return nil, err
	}
	if result.Minimum, err = copyFloat64(schema.Minimum); err != nil {
		return nil, err
	}
	if result.Maximum, err = copyFloat64(schema.Maximum); err != nil {
		return nil, err
	}
	if schema.Items != nil {
		result.Items, err = convertJSONSchemaToGenai(schema.Items)
		if err != nil {
			return nil, err
		}
	}
	if len(schema.Properties) != 0 {
		result.Properties = make(map[string]*genai.Schema, len(schema.Properties))
		for name, property := range schema.Properties {
			converted, err := convertJSONSchemaToGenai(property)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			result.Properties[name] = converted
		}
	}
	result.Required = append([]string(nil), schema.Required...)
	return result, nil
}

func genaiType(value string) (genai.Type, error) {
	switch value {
	case "string":
		return genai.TypeString, nil
	case "number":
		return genai.TypeNumber, nil
	case "integer":
		return genai.TypeInteger, nil
	case "boolean":
		return genai.TypeBoolean, nil
	case "array":
		return genai.TypeArray, nil
	case "object":
		return genai.TypeObject, nil
	default:
		return "", fmt.Errorf("unsupported schema type %q", value)
	}
}

func validateOutputSchemaRoot(schema *jsonschema.Schema) error {
	converted, err := convertJSONSchemaToGenai(schema)
	if err != nil {
		return err
	}
	if converted.Type != genai.TypeObject {
		return fmt.Errorf("root type is %q, want %q", converted.Type, genai.TypeObject)
	}
	return nil
}
