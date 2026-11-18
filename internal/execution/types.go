package execution

import (
	"encoding/json"
	"time"

	deployment "github.com/ml8s/agentgo/internal/deployment"

	"github.com/ml8s/agentgo/internal/domain"
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
	RoleTool      Role = "tool"
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
	// ToolCalls are present only on assistant messages that requested
	// client-executed tools. Args are canonical JSON.
	ToolCalls []ToolCall
	// ToolCallID and ToolOutput describe a tool-message response to a prior
	// assistant tool call. They support protocol resume reconstruction.
	ToolCallID string
	ToolOutput json.RawMessage
	ToolError  string
}

// ToolCall is a runtime-neutral projection of an assistant function call.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// PendingClientTool describes an assistant request that stopped the run and is
// waiting for the trusted protocol client to obtain approval and execute it.
type PendingClientTool struct {
	ToolCallID string
	ToolName   string
	Args       json.RawMessage
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

// RunRequest is the runtime-neutral input of one execution.
type RunRequest struct {
	RunID       string
	ThreadID    string
	UserID      string
	Protocol    domain.Protocol
	UserMessage string
	History     []Message
	Context     json.RawMessage
	// ClientTools are UI tools declared by the trusted product gateway. The
	// runtime exposes them to the model and pauses on their calls until the
	// trusted client resolves them; it never implements their semantics.
	ClientTools []ClientTool
	// Resume carries protocol-level resolutions of pending client tools. A
	// resumed run has no new user message.
	Resume []ClientToolResolution
}

// ClientTool is the runtime-neutral projection of an AG-UI client tool.
type ClientTool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ClientToolResolution is a runtime-neutral projection of an AG-UI resume
// entry. Business payloads and authorization remain with the protocol client.
type ClientToolResolution struct {
	InterruptID string
	ToolCallID  string
	ToolName    string
	Resolved    bool
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
	Definition deployment.DeploymentRef
	Output     json.RawMessage
	Text       string
	Usage      RunUsage
	// PendingClientTools is non-empty when the run paused on one or more
	// client tools awaiting protocol-client execution.
	PendingClientTools []PendingClientTool
}

// RunUsage aggregates model usage for one run (counters summed across every
// finished model call).
type RunUsage struct {
	LLMCalls          int               `json:"llm_calls"`
	PromptTokens      int               `json:"prompt_tokens"`
	CompletionTokens  int               `json:"completion_tokens"`
	ThoughtTokens     int               `json:"thought_tokens"`
	TotalTokens       int               `json:"total_tokens"`
	CachedInputTokens int               `json:"cached_input_tokens"`
	Models            []ModelTokenUsage `json:"models"`
}

// ModelTokenUsage is the usage domain fact for one provider/model pair. A
// multi-agent run must not collapse token evidence into the entrypoint model.
type ModelTokenUsage struct {
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	LLMCalls          int    `json:"llm_calls"`
	PromptTokens      int    `json:"prompt_tokens"`
	CompletionTokens  int    `json:"completion_tokens"`
	ThoughtTokens     int    `json:"thought_tokens"`
	TotalTokens       int    `json:"total_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens"`
}

// StructuredOutputStateKey returns the ADK session-state key that holds an
// Agent's parsed structured model output. Keys are Agent-scoped so delegated
// structured Agents cannot overwrite the entrypoint result.
func StructuredOutputStateKey(agentName string) string {
	return structuredOutputStateKeyPrefix + agentName
}

// Metrics is the runtime observation interface for execution evidence.
type Metrics interface {
	ObserveLLMCall(model, status string, usage LLMTokenUsage)
	ObserveLLMCallLatency(model, status string, duration time.Duration)
	ObserveToolCall(agent, tool, status string, duration time.Duration)
	ObserveAgentDelegation(caller, target, status string, duration time.Duration)
	ObserveMCPCatalog(server, outcome string)
	ObserveRunRejection(reason string)
	ObserveRunTerminalReconciliation(status string, duration time.Duration)
	ObservePendingTerminalRuns(delta int)
	ObserveRunReservation(result string)
}
