package agent

import

// Agent 依赖图（定义聚合）的构建：按 AgentDefinition 依赖序构建 ADK
// Agent 图。记忆化避免重复构建，building 集合检测委派环；白名单未覆盖
// 的 MCP 服务器不进入对应 Agent 的工具集（deny-by-default）。
agentdeployment "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"fmt"

	"github.com/ml8s/liki-agent-runtime/internal/execution"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// agentGraphDeps 聚合图构建所需的协作件（由 NewRuntime 装配）。
type agentGraphDeps struct {
	mcpToolsets       []tool.Toolset
	modelName         string
	resolveModel      func(string) (model.LLM, error)
	temperature       float32
	maxOutputTokens   int
	callAuditor       *llmCallAuditor
	delegationAuditor *DelegationAuditor
	toolAuditor       *ToolExecutionAuditor
}

// agentGraphBuilder 构建以入口定义为根的 Agent 依赖图。
type agentGraphBuilder struct {
	deployment     *agentdeployment.AgentDeployment
	entrypointName string
	deps           agentGraphDeps
	built          map[string]agent.Agent
	building       map[string]struct{}
}

func newAgentGraphBuilder(deployment *agentdeployment.AgentDeployment, entrypointName string, deps agentGraphDeps) *agentGraphBuilder {
	return &agentGraphBuilder{
		deployment:     deployment,
		entrypointName: entrypointName,
		deps:           deps,
		built:          make(map[string]agent.Agent, len(deployment.Spec.Agents)),
		building:       make(map[string]struct{}),
	}
}

// build 依依赖序构建一个 Agent 及其全部子代理（记忆化）。
func (b *agentGraphBuilder) build(definition *agentdeployment.ResolvedAgentDefinition) (agent.Agent, error) {
	if built, ok := b.built[definition.Name]; ok {
		return built, nil
	}
	if _, active := b.building[definition.Name]; active {
		return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("agent delegation cycle through %q", definition.Name), nil)
	}
	b.building[definition.Name] = struct{}{}
	outputKey := ""
	if definition.Output.Structured() {
		outputKey = execution.StructuredOutputStateKey(definition.Name)
	}
	declaration := &definition.AgentDefinition
	subAgents := make([]agent.Agent, 0, len(declaration.SubAgents))
	for _, reference := range declaration.SubAgents {
		target, ok := b.deployment.Agent(reference.Name)
		if !ok {
			return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("sub-agent %q is not defined", reference.Name), nil)
		}
		subAgent, err := b.build(target)
		if err != nil {
			return nil, err
		}
		subAgents = append(subAgents, subAgent)
	}
	toolsets := make([]tool.Toolset, 0, len(b.deployment.Spec.MCPServers))
	for index, server := range b.deployment.Spec.MCPServers {
		allowed := declaration.Tools.Allow[server.Name]
		if len(allowed) == 0 {
			continue
		}
		toolsets = append(toolsets, tool.FilterToolset(
			b.deps.mcpToolsets[index],
			tool.AllowedToolsPredicate(allowed),
		))
	}
	if binding := skillBinding(declaration); binding != nil {
		skillToolset, err := newSkillToolset(binding)
		if err != nil {
			return nil, domain.NewError(domain.CodeAgentDefinitionInvalid, fmt.Sprintf("build skill toolset for agent %q: %v", definition.Name, err), nil)
		}
		toolsets = append(toolsets, skillToolset)
	}
	delete(b.building, definition.Name)
	// Per-Agent model settings fall back to the runtime-wide defaults.
	aiModel, err := b.deps.resolveModel(declaration.EffectiveModel(b.deps.modelName))
	if err != nil {
		return nil, err
	}
	runtimeDefinition, err := NewRuntimeAgentDefinition(*definition)
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, fmt.Sprintf("build runtime agent %q", declaration.Name), err)
	}
	built, err := llmagent.New(runtimeDefinition.ADKConfig(ADKAgentRuntime{
		Model:           aiModel,
		Toolsets:        toolsets,
		SubAgents:       subAgents,
		Temperature:     declaration.EffectiveTemperature(b.deps.temperature),
		MaxOutputTokens: declaration.EffectiveMaxOutputTokens(b.deps.maxOutputTokens),
		OutputKey:       outputKey,
		IsEntrypoint:    declaration.Name == b.entrypointName,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			b.deps.callAuditor.beforeModel,
		},
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			b.deps.callAuditor.afterModel,
		},
		OnModelErrorCallbacks: []llmagent.OnModelErrorCallback{
			b.deps.callAuditor.onModelError,
		},
		BeforeAgentCallbacks: []agent.BeforeAgentCallback{
			b.deps.delegationAuditor.BeforeAgent,
		},
		AfterAgentCallbacks: []agent.AfterAgentCallback{
			b.deps.delegationAuditor.AfterAgent,
		},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{
			b.deps.toolAuditor.BeforeTool,
		},
		AfterToolCallbacks: []llmagent.AfterToolCallback{
			b.deps.toolAuditor.AfterTool,
		},
	}))
	if err != nil {
		return nil, domain.NewError(domain.CodeRuntimeInitFailed, fmt.Sprintf("create ADK agent %q", declaration.Name), err)
	}
	b.built[declaration.Name] = built
	return built, nil
}
