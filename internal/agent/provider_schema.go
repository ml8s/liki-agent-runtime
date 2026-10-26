package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/genai"
)

// ConvertJSONSchemaToGenai maps a neutral JSON Schema into the provider schema
// representation used by ADK.
func ConvertJSONSchemaToGenai(schema *jsonschema.Schema) (*genai.Schema, error) {
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
		result.Items, err = ConvertJSONSchemaToGenai(schema.Items)
		if err != nil {
			return nil, err
		}
	}
	if len(schema.Properties) != 0 {
		result.Properties = make(map[string]*genai.Schema, len(schema.Properties))
		for name, property := range schema.Properties {
			converted, err := ConvertJSONSchemaToGenai(property)
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
