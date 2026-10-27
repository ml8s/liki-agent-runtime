package domain

// Error codes are stable, externally visible identifiers. Protocol adapters
// and audit records reference these constants; never use bare strings.
// Codes are grouped by the layer that raises them.
const (
	// Domain validation codes (internal/domain)
	CodeLLMCallIDRequired        = "llm_call_id_required"
	CodeRunIDRequired            = "run_id_required"
	CodeRunIDInvalid             = "run_id_invalid"
	CodeThreadIDRequired         = "thread_id_required"
	CodeThreadIDInvalid          = "thread_id_invalid"
	CodeUserIDRequired           = "user_id_required"
	CodeUserIDInvalid            = "user_id_invalid"
	CodeAgentNameRequired        = "agent_name_required"
	CodeModelRequired            = "model_required"
	CodeLLMCallStartedAtRequired = "llm_call_started_at_required"
	CodeLLMCallFailed            = "llm_call_failed"

	// Runtime initialization codes (internal/agent constructor)
	CodeLLMModelMissing                   = "llm_model_missing"
	CodeMCPEndpointEnvMissing             = "mcp_endpoint_env_missing"
	CodeMCPEndpointInvalid                = "mcp_endpoint_invalid"
	CodeLLMUnavailable                    = "llm_unavailable"
	CodeRuntimeInitFailed                 = "runtime_init_failed"
	CodeMCPToolsUnavailable               = "mcp_tools_unavailable"
	CodeToolCallIDRequired                = "tool_call_id_required"
	CodeToolCallInvalid                   = "tool_call_invalid"
	CodeToolCallUnknown                   = "tool_call_unknown"
	CodeToolProvenanceInvalid             = "tool_provenance_invalid"
	CodeToolExecutionFailed               = "tool_execution_failed"
	CodeToolExecutionInterrupted          = "tool_execution_interrupted"
	CodeDelegationUnknown                 = "agent_delegation_unknown"
	CodeDelegationFailed                  = "agent_delegation_failed"
	CodeAuditRecorderMissing              = "audit_recorder_missing"
	CodeAgentDefinitionMissing            = "agent_definition_missing"
	CodeAgentDefinitionInvalid            = "agent_definition_invalid"
	CodeStructuredOutputCapabilityInvalid = "structured_output_capability_invalid"

	// Runtime execution codes (internal/agent Run / audit)
	CodeRuntimeCancelled          = "runtime_cancelled"
	CodeRuntimeBusy               = "runtime_busy"
	CodeRuntimeTimeout            = "runtime_timeout"
	CodeRuntimeFailed             = "runtime_failed"
	CodeRuntimeEmptyResponse      = "runtime_empty_response"
	CodeUserMessageRequired       = "user_message_required"
	CodeHistoryInvalid            = "history_invalid"
	CodeRuntimeSessionUnavailable = "runtime_session_unavailable"
	CodeRuntimeInterrupted        = "runtime_interrupted"
	CodeRuntimePaused             = "runtime_paused"
	CodeRunIDConflict             = "run_id_conflict"
	CodeStructuredOutputInvalid   = "structured_output_invalid"
	CodeStructuredOutputEmpty     = "structured_output_empty"

	// Audit lifecycle codes (internal/agent BeginAuditRun/EndAuditRun)
	CodeAuditScopeMissing    = "audit_scope_missing"
	CodeAuditSessionRequired = "audit_session_required"
	CodeAuditSessionActive   = "audit_session_active"

	// Transport / protocol error codes
	CodeUnauthorized     = "unauthorized"
	CodeIdentityRequired = "identity_required"
	CodeRateLimited      = "rate_limited"
	CodeMethodNotAllowed = "method_not_allowed"
)

// Structured output model adapter codes (internal/agent/structured_output.go)
const (
	CodeLLMRequestInvalid = "llm_request_invalid"
)
