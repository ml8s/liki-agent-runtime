package agent

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
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type runState struct {
	output       OutputResult
	capturePlain bool
	tools        toolNameTracker
}

// OutputResult is the validated final outcome of a run: the generic structured
// JSON when declared, plus the user-facing text.
type OutputResult struct {
	JSON json.RawMessage
	Text string
	// Document is the decoded, number-preserving form that JSON was
	// canonicalized from. It is derived data for adapters that need the parsed
	// value: JSON stays the canonical product and the wire contract.
	Document any
}

type toolNameTracker struct {
	seen []string
}

func (t *toolNameTracker) consume(event *session.Event) {
	if event == nil || event.Content == nil {
		return
	}
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionResponse == nil {
			continue
		}
		name := part.FunctionResponse.Name
		seen := false
		for _, value := range t.seen {
			if value == name {
				seen = true
				break
			}
		}
		if !seen {
			t.seen = append(t.seen, name)
		}
	}
}

func (t *toolNameTracker) Names() []string {
	result := make([]string, len(t.seen))
	copy(result, t.seen)
	return result
}

func (s *runState) consume(event *session.Event) {
	s.tools.consume(event)
	// A delegated plain-text agent authors the user-facing final answer even
	// though the entrypoint remains the deployment's root. Capture the last
	// terminal model response across the delegated tree.
	if !s.capturePlain || event == nil || event.Partial || !event.IsFinalResponse() {
		return
	}
	if event.Content == nil {
		return
	}
	var text strings.Builder
	for _, part := range event.Content.Parts {
		if part == nil || part.Text == "" || part.Thought {
			continue
		}
		text.WriteString(part.Text)
	}
	if value := strings.TrimSpace(text.String()); value != "" {
		s.output = OutputResult{Text: value}
	}
}

func (s *runState) consumeStructuredOutput(value any, definition *AgentDefinition) error {
	result, err := ParseStructuredOutput(value, definition)
	if err != nil {
		return err
	}
	s.output = result
	return nil
}

// ParseStructuredOutput is the single protocol-independent finalizer for ADK
// structured-output state. Every protocol receives the same validation,
// canonical JSON, and user-facing pointer text.
func ParseStructuredOutput(value any, definition *AgentDefinition) (OutputResult, error) {
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
	return OutputResult{JSON: canonicalJSON(document), Text: text, Document: document}, nil
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

func canonicalJSON(value any) json.RawMessage {
	raw, err := audit.CanonicalJSON(value)
	if err != nil {
		return []byte("null")
	}
	return raw
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

func buildUserContent(request RunRequest) *genai.Content {
	var prompt strings.Builder
	prompt.WriteString("<request_context>")
	if len(request.Context) != 0 && json.Valid(request.Context) {
		prompt.Write(request.Context)
	} else {
		prompt.WriteString("{}")
	}
	prompt.WriteString("</request_context>\n<user_message>")
	writeJSONString(&prompt, request.UserMessage)
	prompt.WriteString("</user_message>\n")
	return genai.NewContentFromText(prompt.String(), genai.RoleUser)
}

func writeJSONString(prompt *strings.Builder, value string) {
	// json.Marshal handles quotes, backslashes, control characters, and UTF-8
	// escaping. Untrusted text therefore cannot close the surrounding framing.
	encoded, err := json.Marshal(value)
	if err != nil {
		// string marshalling is total; retain content rather than fail the run.
		prompt.WriteString(`"unable-to-encode-message"`)
		return
	}
	prompt.Write(encoded)
}
