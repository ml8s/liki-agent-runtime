package agent

// This file adapts provider-specific structured output wire formats. ADK's
// OpenAI model maps ResponseSchema to json_schema; some OpenAI-compatible
// providers only expose json_object while retaining the same output semantics.

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/ml8s/agent-runtime/internal/domain"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

const structuredOutputInstructionPrefix = `Return exactly one JSON object that conforms to this JSON Schema. ` +
	`Do not return Markdown, explanations, or text outside the JSON object.` +
	"\nJSON Schema:\n"

const systemRole = "system"

// jsonObjectModel wraps an ADK model for providers that support JSON mode but
// not OpenAI's strict json_schema response format. It is a model adapter: the
// agent still owns one OutputSchema, and downstream parsing does not change.
type jsonObjectModel struct {
	delegate model.LLM
	name     string
}

func newJSONObjectModel(delegate model.LLM) (*jsonObjectModel, error) {
	if delegate == nil {
		return nil, fmt.Errorf("%w: delegate model is required", domain.ErrInvalidInput)
	}
	return &jsonObjectModel{
		delegate: delegate,
		name:     delegate.Name(),
	}, nil
}

func (m *jsonObjectModel) Name() string { return m.name }

func (m *jsonObjectModel) GenerateContent(
	ctx context.Context,
	req *model.LLMRequest,
	stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		adapted, structured, err := m.adaptRequest(req)
		if err != nil {
			yield(nil, domain.NewError(domain.CodeLLMRequestInvalid, "adapt structured output request", err))
			return
		}
		for response, err := range m.delegate.GenerateContent(ctx, adapted, stream) {
			if err == nil && structured {
				response = m.normalizeResponse(response)
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

func (m *jsonObjectModel) adaptRequest(req *model.LLMRequest) (*model.LLMRequest, bool, error) {
	if req == nil {
		return nil, false, fmt.Errorf("%w: LLM request is required", domain.ErrInvalidInput)
	}
	if req.Config == nil {
		return req, false, nil
	}
	var schema any
	if req.Config.ResponseSchema != nil {
		schema = req.Config.ResponseSchema
	} else if req.Config.ResponseJsonSchema != nil {
		schema = req.Config.ResponseJsonSchema
	} else {
		return req, false, nil
	}
	adaptedConfig := *req.Config
	adaptedConfig.ResponseSchema = nil
	adaptedConfig.ResponseJsonSchema = nil
	adaptedConfig.ResponseMIMEType = "application/json"
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, false, fmt.Errorf("marshal structured output schema: %w", err)
	}
	instruction := structuredOutputInstructionPrefix + string(raw)
	if req.Config.SystemInstruction == nil {
		adaptedConfig.SystemInstruction = genai.NewContentFromText(instruction, systemRole)
	} else {
		parts := make([]*genai.Part, 0, len(req.Config.SystemInstruction.Parts)+1)
		parts = append(parts, req.Config.SystemInstruction.Parts...)
		parts = append(parts, &genai.Part{Text: instruction})
		role := req.Config.SystemInstruction.Role
		if role == "" {
			role = systemRole
		}
		adaptedConfig.SystemInstruction = &genai.Content{Role: role, Parts: parts}
	}

	adapted := *req
	adapted.Config = &adaptedConfig
	return &adapted, true, nil
}

// normalizeResponse handles providers that accept json_object but still emit
// short commentary around the requested JSON object. It performs a boundary
// adaptation only: the extracted payload must be syntactically valid JSON, and
// ADK still performs schema validation. Thought and function-call parts are
// preserved unchanged.
func (m *jsonObjectModel) normalizeResponse(response *model.LLMResponse) *model.LLMResponse {
	if response == nil || response.Content == nil || len(response.Content.Parts) == 0 {
		return response
	}
	var text strings.Builder
	kept := make([]*genai.Part, 0, len(response.Content.Parts))
	hasPayloadText := false
	for _, part := range response.Content.Parts {
		if part == nil {
			continue
		}
		if part.Text != "" && !part.Thought {
			text.WriteString(part.Text)
			hasPayloadText = true
			continue
		}
		kept = append(kept, part)
	}
	if !hasPayloadText {
		return response
	}
	payload, ok := extractJSONObject(text.String())
	if !ok {
		return response
	}
	kept = append(kept, &genai.Part{Text: payload})
	normalized := *response
	normalized.Content = &genai.Content{Role: response.Content.Role, Parts: kept}
	return &normalized
}

// extractJSONObject returns the first syntactically valid top-level JSON
// object. Structural characters inside JSON strings and escape sequences are
// honored, so braces in Chinese prose or values do not terminate extraction.
func extractJSONObject(raw string) (string, bool) {
	var (
		start   = -1
		depth   int
		inText  bool
		escaped bool
	)
	for index, char := range raw {
		if inText {
			if escaped {
				escaped = false
				continue
			}
			switch char {
			case '\\':
				escaped = true
			case '"':
				inText = false
			}
			continue
		}
		switch char {
		case '"':
			if depth > 0 {
				inText = true
			}
		case '{':
			if depth == 0 {
				start = index
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				payload := raw[start : index+1]
				if json.Valid([]byte(payload)) {
					return payload, true
				}
				start = -1
			}
		}
	}
	return "", false
}
