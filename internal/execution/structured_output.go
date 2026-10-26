package execution

import deployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ml8s/liki-agent-runtime/internal/audit"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	jsonpointer "github.com/qri-io/jsonpointer"
)

// ParseStructuredOutput is the single protocol-independent finalizer for ADK
// structured-output state. Every protocol receives the same validation,
// canonical JSON, and user-facing pointer text.
func ParseStructuredOutput(value any, definition *deployment.ResolvedAgentDefinition) (OutputResult, error) {
	if definition == nil || definition.ResolvedOutput == nil {
		return OutputResult{}, domain.NewError(
			domain.CodeStructuredOutputInvalid,
			"structured output schema is not configured",
			nil,
		)
	}
	var document any
	switch typed := value.(type) {
	case string:
		var err error
		if document, err = decodeJSONDocument([]byte(typed)); err != nil {
			return OutputResult{}, domain.NewError(domain.CodeStructuredOutputInvalid, "decode structured agent output", err)
		}
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return OutputResult{}, domain.NewError(domain.CodeStructuredOutputInvalid, "encode structured agent output", err)
		}
		if document, err = decodeJSONDocument(raw); err != nil {
			return OutputResult{}, domain.NewError(domain.CodeStructuredOutputInvalid, "decode structured agent output", err)
		}
	}
	if err := definition.ResolvedOutput.Validate(schemaValue(document)); err != nil {
		return OutputResult{}, domain.NewError(domain.CodeStructuredOutputInvalid, "validate structured agent output", err)
	}
	text, err := jsonStringAtPointer(document, definition.Output.TextPointer)
	if err != nil {
		return OutputResult{}, domain.NewError(domain.CodeStructuredOutputInvalid, "extract structured answer text", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return OutputResult{}, domain.NewError(domain.CodeStructuredOutputEmpty, "structured agent output has no answer text", nil)
	}
	canonical, err := canonicalJSON(document)
	if err != nil {
		return OutputResult{}, domain.NewError(
			domain.CodeStructuredOutputInvalid,
			"canonicalize structured agent output",
			err,
		)
	}
	return OutputResult{JSON: canonical, Text: text, Document: document}, nil
}

func jsonStringAtPointer(document any, pointer string) (string, error) {
	pointerValue, err := jsonpointer.Parse(pointer)
	if err != nil {
		return "", fmt.Errorf("parse JSON Pointer: %w", err)
	}
	if pointerValue.IsEmpty() {
		if text, ok := document.(string); ok {
			return text, nil
		}
		return "", fmt.Errorf("root value is not a string")
	}
	value, err := pointerValue.Eval(document)
	if err != nil {
		return "", fmt.Errorf("evaluate JSON Pointer: %w", err)
	}
	current := value
	text, ok := current.(string)
	if !ok {
		return "", fmt.Errorf("pointer %q does not select a string", pointer)
	}
	return text, nil
}

func canonicalJSON(value any) (json.RawMessage, error) {
	return audit.CanonicalJSON(value)
}

func decodeJSONDocument(raw []byte) (any, error) {
	// bytes.NewReader keeps decoding zero-copy; wrapping the bytes in a
	// string would duplicate the whole document before parsing it.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return document, nil
}

func schemaValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = schemaValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = schemaValue(item)
		}
		return result
	default:
		return value
	}
}
