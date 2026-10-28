package execution

import (
	"errors"
	"strings"
	"time"

	"github.com/ml8s/agent-runtime/internal/domain"
)

// LLMCallStatus is the lifecycle state of one model call.
type LLMCallStatus string

// Model call lifecycle states.
const (
	LLMCallRunning   LLMCallStatus = "running"
	LLMCallCompleted LLMCallStatus = "completed"
	LLMCallFailed    LLMCallStatus = "failed"
)

// LLMTokenUsage is the token accounting for one model call.
type LLMTokenUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	ThoughtTokens    int64
	TotalTokens      int64
}

// LLMCall is one auditable model invocation inside a run.
type LLMCall struct {
	ID                    string
	RunID                 domain.ID
	ThreadID              domain.ID
	UserID                string
	AgentName             string
	AgentVersion          string
	AgentDefinitionDigest string
	Model                 string
	Provider              string
	Status                LLMCallStatus
	PromptTokens          int64
	CompletionTokens      int64
	ThoughtTokens         int64
	TotalTokens           int64
	DurationMS            int64
	ErrorCode             string
	ErrorMessage          string
	PromptVersion         string
	DefinitionName        string
	DefinitionVersion     string
	DefinitionDigest      string
	StartedAt             time.Time
	FinishedAt            time.Time
}

// NormalizeLLMCall returns a normalized copy of the call. The input call is
// never modified.
func NormalizeLLMCall(call LLMCall) LLMCall {
	call.ID = strings.TrimSpace(call.ID)
	call.AgentName = strings.TrimSpace(call.AgentName)
	call.Model = strings.TrimSpace(call.Model)
	call.ErrorCode = strings.TrimSpace(call.ErrorCode)
	return call
}

// ValidateLLMCall reports whether the call carries the required provenance.
func ValidateLLMCall(call LLMCall) error {
	switch {
	case call.ID == "":
		return domain.NewError(domain.CodeLLMCallIDRequired, "llm call id is required", domain.ErrInvalidInput)
	case call.RunID == "":
		return domain.NewError(domain.CodeRunIDRequired, "run_id is required", domain.ErrInvalidInput)
	case call.ThreadID == "":
		return domain.NewError(domain.CodeThreadIDRequired, "thread_id is required", domain.ErrInvalidInput)
	case call.UserID == "":
		return domain.NewError(domain.CodeUserIDRequired, "user_id is required", domain.ErrInvalidInput)
	case call.AgentName == "":
		return domain.NewError(domain.CodeAgentNameRequired, "agent_name is required", domain.ErrInvalidInput)
	case call.Model == "":
		return domain.NewError(domain.CodeModelRequired, "model is required", domain.ErrInvalidInput)
	case call.StartedAt.IsZero():
		return domain.NewError(domain.CodeLLMCallStartedAtRequired, "llm call start time is required", domain.ErrInvalidInput)
	}
	return nil
}

// CompleteLLMCall returns the succeeded terminal form of a running call.
func CompleteLLMCall(
	call LLMCall,
	usage LLMTokenUsage,
	finishedAt time.Time,
) LLMCall {
	next := call
	next.Status = LLMCallCompleted
	next.PromptTokens = usage.PromptTokens
	next.CompletionTokens = usage.CompletionTokens
	next.ThoughtTokens = usage.ThoughtTokens
	next.TotalTokens = usage.TotalTokens
	next.FinishedAt = finishedAt
	next.DurationMS = finishedAt.Sub(call.StartedAt).Milliseconds()
	return next
}

// FailLLMCall returns the failed terminal form of a running call.
func FailLLMCall(call LLMCall, err error, finishedAt time.Time) LLMCall {
	next := call
	next.Status = LLMCallFailed
	next.FinishedAt = finishedAt
	next.DurationMS = finishedAt.Sub(call.StartedAt).Milliseconds()
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		next.ErrorCode = domainErr.Code
		next.ErrorMessage = domainErr.Message
		return next
	}
	next.ErrorCode = domain.CodeLLMCallFailed
	next.ErrorMessage = "LLM call failed"
	return next
}
