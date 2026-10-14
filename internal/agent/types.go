package agent

import (
	"encoding/json"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

// Role is a conversation-message role.
type Role string

// Conversation message roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// MaxHistoryMessages prevents a protocol history from becoming an
// unbounded reconstructed prompt.
const (
	MaxHistoryMessages = 100
)

// Message is one conversation-history turn passed into a run.
type Message struct {
	Role    Role
	Content string
}

// RunRequest is the runtime-neutral input of one execution.
type RunRequest struct {
	RunID       string
	ThreadID    string
	UserID      string
	Protocol    string
	UserMessage string
	History     []Message
	Context     json.RawMessage
}

// RunResult is the validated outcome of one execution.
type RunResult struct {
	Definition DefinitionRef
	Output     json.RawMessage
	Text       string
}

// StructuredOutputStateKey returns the ADK session-state key that holds an
// Agent's parsed structured model output. Keys are Agent-scoped so delegated
// structured Agents cannot overwrite the entrypoint result.
func StructuredOutputStateKey(agentName string) string {
	return structuredOutputStateKeyPrefix + agentName
}

// Metrics is the runtime observation interface for execution evidence.
type Metrics interface {
	ObserveLLMCall(model, status string, usage domain.LLMTokenUsage)
	ObserveToolCall(agent, tool, status string, duration time.Duration)
	ObserveAgentDelegation(caller, target, status string, duration time.Duration)
}

// ValidIdentifier accepts bounded printable identifiers while preserving
// provider-specific identifier alphabets.
func ValidIdentifier(value string) bool {
	return domain.ValidIdentifier(value)
}
