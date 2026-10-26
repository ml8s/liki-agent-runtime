package domain

import (
	"errors"
	"strings"
	"time"
)

// LLMCallStatus is the lifecycle state of one model call.
type LLMCallStatus string

// ID is a logical cross-service identifier. The agent does not own foreign
// tables; IDs are opaque audit correlation values.
type ID string

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
	RunID                 ID
	ThreadID              ID
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
		return NewError(CodeLLMCallIDRequired, "llm call id is required", ErrInvalidInput)
	case call.RunID == "":
		return NewError(CodeRunIDRequired, "run_id is required", ErrInvalidInput)
	case call.ThreadID == "":
		return NewError(CodeThreadIDRequired, "thread_id is required", ErrInvalidInput)
	case call.UserID == "":
		return NewError(CodeUserIDRequired, "user_id is required", ErrInvalidInput)
	case call.AgentName == "":
		return NewError(CodeAgentNameRequired, "agent_name is required", ErrInvalidInput)
	case call.Model == "":
		return NewError(CodeModelRequired, "model is required", ErrInvalidInput)
	case call.StartedAt.IsZero():
		return NewError(CodeLLMCallStartedAtRequired, "llm call start time is required", ErrInvalidInput)
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
	var domainErr *Error
	if errors.As(err, &domainErr) {
		next.ErrorCode = domainErr.Code
		next.ErrorMessage = domainErr.Message
		return next
	}
	next.ErrorCode = CodeLLMCallFailed
	next.ErrorMessage = "LLM call failed"
	return next
}
