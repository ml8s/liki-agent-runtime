package execution

import deployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"encoding/json"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

// structuredOutputStateKeyPrefix matches llmagent.Config.OutputKey. ADK parses
// the model reply against OutputSchema, clears Event.Output before yielding it,
// and persists the parsed value into session state under this key.
const structuredOutputStateKeyPrefix = "structured_analysis:"

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

// Identity projects caller-owned request identity for lifecycle validation.
func (r RunRequest) Identity() RunIdentity {
	return RunIdentity{
		RunID:    domain.ID(r.RunID),
		ThreadID: domain.ID(r.ThreadID),
		UserID:   r.UserID,
	}
}

// RunResult is the validated outcome of one execution.
type RunResult struct {
	Definition deployment.DefinitionRef
	Output     json.RawMessage
	Text       string
	Usage      RunUsage
}

// RunUsage aggregates model usage for one run (counters summed across every
// finished model call).
type RunUsage struct {
	LLMCalls         int
	PromptTokens     int
	CompletionTokens int
	ThoughtTokens    int
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
	ObserveLLMCallLatency(model, status string, duration time.Duration)
	ObserveToolCall(agent, tool, status string, duration time.Duration)
	ObserveAgentDelegation(caller, target, status string, duration time.Duration)
	ObserveMCPCatalog(server, outcome string)
	ObserveRunRejection(reason string)
}

// ValidIdentifier accepts bounded printable identifiers while preserving
// provider-specific identifier alphabets.
func ValidIdentifier(value string) bool {
	return domain.ValidIdentifier(value)
}
