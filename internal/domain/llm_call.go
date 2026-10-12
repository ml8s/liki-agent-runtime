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

// Normalize trims whitespace from identifier and code fields.
func (c *LLMCall) Normalize() {
	c.ID = strings.TrimSpace(c.ID)
	c.AgentName = strings.TrimSpace(c.AgentName)
	c.Model = strings.TrimSpace(c.Model)
	c.ErrorCode = strings.TrimSpace(c.ErrorCode)
}

// Validate reports whether the call carries the required provenance.
func (c *LLMCall) Validate() error {
	switch {
	case c.ID == "":
		return NewError(CodeLLMCallIDRequired, "llm call id is required", ErrInvalidInput)
	case c.RunID == "":
		return NewError(CodeRunIDRequired, "run_id is required", ErrInvalidInput)
	case c.ThreadID == "":
		return NewError(CodeThreadIDRequired, "thread_id is required", ErrInvalidInput)
	case c.UserID == "":
		return NewError(CodeUserIDRequired, "user_id is required", ErrInvalidInput)
	case c.AgentName == "":
		return NewError(CodeAgentNameRequired, "agent_name is required", ErrInvalidInput)
	case c.Model == "":
		return NewError(CodeModelRequired, "model is required", ErrInvalidInput)
	case c.StartedAt.IsZero():
		return NewError(CodeLLMCallStartedAtRequired, "llm call start time is required", ErrInvalidInput)
	}
	return nil
}

// Complete marks the call succeeded and records its token usage.
func (c *LLMCall) Complete(usage LLMTokenUsage, finishedAt time.Time) {
	c.Status = LLMCallCompleted
	c.PromptTokens = usage.PromptTokens
	c.CompletionTokens = usage.CompletionTokens
	c.ThoughtTokens = usage.ThoughtTokens
	c.TotalTokens = usage.TotalTokens
	c.FinishedAt = finishedAt
	c.DurationMS = finishedAt.Sub(c.StartedAt).Milliseconds()
}

// Fail marks the call failed with a stable error code.
func (c *LLMCall) Fail(err error, finishedAt time.Time) {
	c.Status = LLMCallFailed
	c.FinishedAt = finishedAt
	c.DurationMS = finishedAt.Sub(c.StartedAt).Milliseconds()
	var domainErr *Error
	if errors.As(err, &domainErr) {
		c.ErrorCode = domainErr.Code
		c.ErrorMessage = domainErr.Message
		return
	}
	c.ErrorCode = CodeLLMCallFailed
	c.ErrorMessage = "LLM call failed"
}
