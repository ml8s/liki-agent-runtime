package agui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	aguitypes "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ml8s/agentgo/internal/execution"
)

// BFF-declared AG-UI client tools are a controlled await-result extension to
// the text-chat profile. The bounds keep model tool declarations small and
// prevent a malformed gateway payload from becoming unbounded runtime state.
const (
	maxClientTools            = 8
	maxClientToolNameLength   = 64
	maxClientToolDescLength   = 1024
	maxClientToolSchemaLength = 16 << 10
)

var clientToolNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)

// toolResult is an authoritative client-executed tool response carried from a
// resume projection to the protocol event stream. It is adapter-local: the
// model receives the same fact through projected history.
type toolResult struct {
	ToolCallID string
	ToolName   string
	Output     json.RawMessage
	Error      string
}

// validateTextChatProfile rejects capabilities this runtime does not consume.
// Explicit rejection prevents silent protocol data loss at the adapter edge.
func validateTextChatProfile(input aguitypes.RunAgentInput) error {
	if input.ParentRunID != nil {
		return fmt.Errorf("parent runs are outside the AG-UI text-chat profile")
	}
	if !isEmptyClientState(input.State) {
		return fmt.Errorf("client state is outside the AG-UI text-chat profile")
	}
	if len(input.Context) != 0 {
		return fmt.Errorf("client context is outside the AG-UI text-chat profile")
	}
	for _, message := range input.Messages {
		switch message.Role {
		case aguitypes.RoleUser, aguitypes.RoleAssistant:
			if len(input.Resume) == 0 {
				if len(message.ToolCalls) != 0 || message.ToolCallID != "" || message.ActivityType != "" || message.SubagentRunID != "" {
					return fmt.Errorf("tool or activity messages are outside the AG-UI text-chat profile")
				}
			} else if message.ActivityType != "" || message.SubagentRunID != "" {
				return fmt.Errorf("activity messages are outside the AG-UI resume profile")
			}
		case aguitypes.RoleTool:
			if len(input.Resume) == 0 {
				return fmt.Errorf("message role %q is outside the AG-UI text-chat profile", message.Role)
			}
			if message.ToolCallID == "" {
				return fmt.Errorf("resume tool message requires toolCallId")
			}
			if message.ActivityType != "" || message.SubagentRunID != "" {
				return fmt.Errorf("activity messages are outside the AG-UI resume profile")
			}
		default:
			return fmt.Errorf("message role %q is outside the AG-UI text-chat profile", message.Role)
		}
	}
	return nil
}

// clientToolsFromInput projects AG-UI client tools into the runtime-neutral
// execution request. Tool behavior is owned by the caller (normally the
// product gateway); the runtime only exposes the declaration and pauses.
func clientToolsFromInput(tools []aguitypes.Tool) ([]execution.ClientTool, error) {
	if len(tools) > maxClientTools {
		return nil, fmt.Errorf("AG-UI request contains %d client tools, maximum is %d", len(tools), maxClientTools)
	}

	clientTools := make([]execution.ClientTool, 0, len(tools))
	names := make(map[string]struct{}, len(tools))
	for _, declared := range tools {
		name := strings.TrimSpace(declared.Name)
		if name == "" || len(name) > maxClientToolNameLength || !clientToolNamePattern.MatchString(name) {
			return nil, fmt.Errorf("client tool name %q is invalid", declared.Name)
		}
		if _, exists := names[name]; exists {
			return nil, fmt.Errorf("client tool name %q is duplicated", name)
		}
		names[name] = struct{}{}

		if strings.TrimSpace(declared.Description) == "" {
			return nil, fmt.Errorf("client tool %q requires a description", name)
		}
		if len(declared.Description) > maxClientToolDescLength {
			return nil, fmt.Errorf("client tool %q description exceeds %d bytes", name, maxClientToolDescLength)
		}

		parameters, err := json.Marshal(declared.Parameters)
		if err != nil {
			return nil, fmt.Errorf("encode client tool %q parameters: %w", name, err)
		}
		if parameters == nil || string(parameters) == "null" {
			parameters = []byte(`{"type":"object"}`)
		}
		if len(parameters) > maxClientToolSchemaLength {
			return nil, fmt.Errorf("client tool %q schema exceeds %d bytes", name, maxClientToolSchemaLength)
		}

		schema := &jsonschema.Schema{}
		if err := json.Unmarshal(parameters, schema); err != nil {
			return nil, fmt.Errorf("decode client tool %q schema: %w", name, err)
		}
		if schema.Type != "object" {
			return nil, fmt.Errorf("client tool %q parameters must have type \"object\"", name)
		}
		if _, err := schema.Resolve(nil); err != nil {
			return nil, fmt.Errorf("validate client tool %q schema: %w", name, err)
		}

		clientTools = append(clientTools, execution.ClientTool{
			Name:        name,
			Description: strings.TrimSpace(declared.Description),
			Parameters:  parameters,
		})
	}
	return clientTools, nil
}

// resumeRunProjection projects a trusted resume stream into runtime-neutral
// history and tool results. ResumeEntry.metadata carries the standard
// interrupt-to-toolCall correlation supplied by the authoritative protocol
// client; payload and business authorization remain owned by that client.
func resumeRunProjection(
	messages []aguitypes.Message,
	resume []aguitypes.ResumeEntry,
) ([]execution.Message, []execution.ClientToolResolution, []toolResult, error) {
	if len(resume) == 0 {
		return nil, nil, nil, fmt.Errorf("resume run requires at least one resume entry")
	}
	history := make([]execution.Message, 0, len(messages))
	resolutions := make([]execution.ClientToolResolution, 0, len(resume))
	results := make([]toolResult, 0, len(resume))

	for _, entry := range resume {
		if entry.InterruptID == "" {
			return nil, nil, nil, fmt.Errorf("resume entry requires interruptId")
		}
		toolCallID, _ := entry.Metadata["tool_call_id"].(string)
		if toolCallID == "" {
			return nil, nil, nil, fmt.Errorf("resume entry %q requires toolCallId correlation metadata", entry.InterruptID)
		}
		resolutions = append(resolutions, execution.ClientToolResolution{
			InterruptID: entry.InterruptID,
			ToolCallID:  toolCallID,
			Resolved:    entry.Status == aguitypes.ResumeStatusResolved,
		})
	}

	for _, message := range messages {
		projected := execution.Message{Role: execution.Role(message.Role), ToolCallID: message.ToolCallID}
		if text, err := conversationText(message.Content); err == nil && text != "" {
			projected.Content = text
		} else if message.Role != aguitypes.RoleTool && err != nil {
			return nil, nil, nil, err
		}
		for _, call := range message.ToolCalls {
			args := json.RawMessage(call.Function.Arguments)
			if !json.Valid(args) {
				return nil, nil, nil, fmt.Errorf("resume tool call %q has invalid arguments", call.ID)
			}
			projected.ToolCalls = append(projected.ToolCalls, execution.ToolCall{
				ID:   call.ID,
				Name: call.Function.Name,
				Args: args,
			})
		}
		if message.Role == aguitypes.RoleTool {
			output, err := rawJSON(message.Content)
			if err != nil {
				return nil, nil, nil, err
			}
			projected.ToolOutput = output
			projected.ToolError = message.Error
		}
		history = append(history, projected)
	}

	for index, resolution := range resolutions {
		if !resolution.Resolved {
			continue
		}
		name := toolNameFromHistory(history, resolution.ToolCallID)
		if name == "" {
			return nil, nil, nil, fmt.Errorf("resume %q references unknown tool call %q", resolution.InterruptID, resolution.ToolCallID)
		}
		if !hasToolResult(messages, resolution.ToolCallID) {
			return nil, nil, nil, fmt.Errorf("resolved resume %q requires a tool result", resolution.InterruptID)
		}
		output, err := toolResultOutput(messages, resolution.ToolCallID)
		if err != nil {
			return nil, nil, nil, err
		}
		resolutions[index].ToolName = name
		results = append(results, toolResult{
			ToolCallID: resolution.ToolCallID,
			ToolName:   name,
			Output:     output,
		})
	}
	return history, resolutions, results, nil
}

func isEmptyClientState(state any) bool {
	if state == nil {
		return true
	}
	switch value := state.(type) {
	case map[string]any:
		return len(value) == 0
	case []any:
		return len(value) == 0
	case string:
		return value == ""
	default:
		return false
	}
}

func lastUserConversation(messages []aguitypes.Message) (string, []execution.Message, error) {
	lastIndex := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == aguitypes.RoleUser {
			lastIndex = index
			break
		}
	}
	if lastIndex < 0 {
		return "", nil, fmt.Errorf("AG-UI request must contain a user message")
	}
	content, err := conversationText(messages[lastIndex].Content)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(content) == "" {
		return "", nil, fmt.Errorf("the latest user message is empty")
	}

	history := make([]execution.Message, 0, len(messages)-1)
	for index, message := range messages {
		if index == lastIndex || message.Role != aguitypes.RoleUser && message.Role != aguitypes.RoleAssistant {
			continue
		}
		text, err := conversationText(message.Content)
		if err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		role := execution.RoleUser
		if message.Role == aguitypes.RoleAssistant {
			role = execution.RoleAssistant
		}
		history = append(history, execution.Message{Role: role, Content: text})
	}
	return content, history, nil
}

func conversationText(content any) (string, error) {
	switch value := content.(type) {
	case string:
		return value, nil
	case []aguitypes.InputContent:
		var result strings.Builder
		for _, fragment := range value {
			if fragment.Type != aguitypes.InputContentTypeText {
				return "", fmt.Errorf("input content type %q is outside the AG-UI text-chat profile", fragment.Type)
			}
			result.WriteString(fragment.Text)
		}
		return result.String(), nil
	case []any:
		var result strings.Builder
		for _, item := range value {
			fragment, ok := item.(map[string]any)
			if !ok {
				return "", fmt.Errorf("message content contains a non-object part")
			}
			if kind, ok := fragment["type"].(string); ok && kind != aguitypes.InputContentTypeText {
				return "", fmt.Errorf("input content type %q is outside the AG-UI text-chat profile", kind)
			}
			if text, ok := fragment["text"].(string); ok {
				result.WriteString(text)
			}
		}
		return result.String(), nil
	default:
		return "", fmt.Errorf("message content is outside the AG-UI text-chat profile")
	}
}

func opaqueJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage("{}")
	}
	return raw
}

func rawJSON(value any) (json.RawMessage, error) {
	switch typed := value.(type) {
	case nil:
		return []byte("null"), nil
	case json.RawMessage:
		return typed, nil
	case string:
		if json.Valid([]byte(typed)) {
			return json.RawMessage(typed), nil
		}
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil, err
		}
		return encoded, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return encoded, nil
	}
}

func toolNameFromHistory(history []execution.Message, toolCallID string) string {
	for _, message := range history {
		for _, call := range message.ToolCalls {
			if call.ID == toolCallID {
				return call.Name
			}
		}
	}
	return ""
}

func hasToolResult(messages []aguitypes.Message, toolCallID string) bool {
	for _, message := range messages {
		if message.Role == aguitypes.RoleTool && message.ToolCallID == toolCallID {
			return true
		}
	}
	return false
}

func toolResultOutput(messages []aguitypes.Message, toolCallID string) (json.RawMessage, error) {
	for _, message := range messages {
		if message.Role != aguitypes.RoleTool || message.ToolCallID != toolCallID {
			continue
		}
		return rawJSON(message.Content)
	}
	return nil, fmt.Errorf("tool result %q is missing", toolCallID)
}
