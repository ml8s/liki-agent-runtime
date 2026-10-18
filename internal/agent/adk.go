package agent

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// adkTransferToolName is ADK's built-in delegation tool. It is not an MCP tool
// and is audited through Agent lifecycle callbacks rather than tool provenance.
const adkTransferToolName = "transfer_to_agent"

// adkFinishTaskToolName is ADK's built-in task-completion tool, installed on
// task-mode Agents. It is framework delegation plumbing, not an MCP tool, and
// is audited through Agent delegation lifecycle callbacks.
const adkFinishTaskToolName = "finish_task"

// ADKAgentRuntime carries the shared runtime objects that are intentionally
// outside the deployment artifact. The artifact never contains credentials,
// model clients, callbacks, or MCP transports.
type ADKAgentRuntime struct {
	RawOutputSchema map[string]any
	Model           model.LLM
	Toolsets        []tool.Toolset
	SubAgents       []agent.Agent
	Temperature     float32
	// MaxOutputTokens bounds the model response; 0 leaves the provider
	// default unbounded.
	MaxOutputTokens int
	OutputKey       string
	IsEntrypoint    bool

	BeforeModelCallbacks  []llmagent.BeforeModelCallback
	AfterModelCallbacks   []llmagent.AfterModelCallback
	OnModelErrorCallbacks []llmagent.OnModelErrorCallback
	BeforeAgentCallbacks  []agent.BeforeAgentCallback
	AfterAgentCallbacks   []agent.AfterAgentCallback
	BeforeToolCallbacks   []llmagent.BeforeToolCallback
	AfterToolCallbacks    []llmagent.AfterToolCallback
}

// ADKConfig is the single translation point from a validated artifact
// definition to ADK's public LlmAgent API.
func (a *AgentDefinition) ADKConfig(runtime ADKAgentRuntime) llmagent.Config {
	beforeModelCallbacks := runtime.BeforeModelCallbacks
	if runtime.RawOutputSchema != nil {
		beforeModelCallbacks = append([]llmagent.BeforeModelCallback{
			rawOutputSchemaCallback(runtime.RawOutputSchema),
		}, beforeModelCallbacks...)
	}
	return llmagent.Config{
		Name:        a.Name,
		Description: a.Description,
		Mode:        adkMode(a.Mode),
		Model:       runtime.Model,
		Instruction: a.InstructionText,
		InstructionProvider: func(_ agent.ReadonlyContext) (string, error) {
			return a.InstructionText, nil
		},
		SubAgents:                runtime.SubAgents,
		Toolsets:                 runtime.Toolsets,
		BeforeModelCallbacks:     beforeModelCallbacks,
		AfterModelCallbacks:      runtime.AfterModelCallbacks,
		OnModelErrorCallbacks:    runtime.OnModelErrorCallbacks,
		BeforeAgentCallbacks:     runtime.BeforeAgentCallbacks,
		AfterAgentCallbacks:      runtime.AfterAgentCallbacks,
		BeforeToolCallbacks:      runtime.BeforeToolCallbacks,
		AfterToolCallbacks:       runtime.AfterToolCallbacks,
		DisallowTransferToParent: runtime.IsEntrypoint,
		DisallowTransferToPeers:  true,
		GenerateContentConfig: &genai.GenerateContentConfig{
			Temperature:     &runtime.Temperature,
			MaxOutputTokens: int32(runtime.MaxOutputTokens),
		},
		OutputSchema: a.GenaiOutputSchema,
		OutputKey:    runtime.OutputKey,
	}
}

// rawOutputSchemaCallback preserves the complete external JSON Schema on the
// ADK OpenAI-compatible wire. ADK's genai.Schema remains responsible for
// framework parsing; the raw schema avoids dropping JSON Schema keywords when
// translating through that internal representation.
func rawOutputSchemaCallback(schema map[string]any) llmagent.BeforeModelCallback {
	return func(_ agent.Context, request *model.LLMRequest) (*model.LLMResponse, error) {
		if request == nil || request.Config == nil || request.Config.ResponseSchema == nil {
			return nil, nil
		}
		request.Config.ResponseJsonSchema = schema
		request.Config.ResponseSchema = nil
		return nil, nil
	}
}

func adkMode(mode AgentMode) llmagent.Mode {
	switch mode {
	case AgentModeTask:
		return llmagent.ModeTask
	case AgentModeSingleTurn:
		return llmagent.ModeSingleTurn
	default:
		return llmagent.ModeChat
	}
}
