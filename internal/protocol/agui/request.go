package agui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	aguitypes "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/ml8s/agent-runtime/internal/execution"
)

// validateTextChatProfile rejects capabilities this runtime does not consume.
// Explicit rejection prevents silent protocol data loss at the adapter edge.
func validateTextChatProfile(input aguitypes.RunAgentInput) error {
	if input.ParentRunID != nil {
		return fmt.Errorf("parent runs are outside the AG-UI text-chat profile")
	}
	if !isEmptyClientState(input.State) {
		return fmt.Errorf("client state is outside the AG-UI text-chat profile")
	}
	if len(input.Tools) != 0 {
		return fmt.Errorf("client tools are outside the AG-UI text-chat profile")
	}
	if len(input.Context) != 0 {
		return fmt.Errorf("client context is outside the AG-UI text-chat profile")
	}
	if len(input.Resume) != 0 {
		return fmt.Errorf("resume is outside the AG-UI text-chat profile")
	}
	for _, message := range input.Messages {
		switch message.Role {
		case aguitypes.RoleUser, aguitypes.RoleAssistant:
		default:
			return fmt.Errorf("message role %q is outside the AG-UI text-chat profile", message.Role)
		}
		if len(message.ToolCalls) != 0 || message.ToolCallID != "" || message.ActivityType != "" || message.SubagentRunID != "" {
			return fmt.Errorf("tool or activity messages are outside the AG-UI text-chat profile")
		}
	}
	return nil
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
				continue
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
