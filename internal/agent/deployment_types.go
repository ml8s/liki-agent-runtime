// Package agent owns the single ADK execution graph, protocol adaptation, and
// durable audit evidence. External AgentDeployment artifacts are the only
// attachment point for domain prompts, output schemas, tool allowlists, and
// Agent identity.
package agent

import (
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/genai"
)

const (
	maxManifestBytes       = 2 << 20
	maxTextArtifactBytes   = 2 << 20
	maxSchemaArtifactBytes = 4 << 20

	// ContractVersion identifies the AgentDeployment JSON shape. Runtime
	// releases reject artifacts written for an incompatible contract.
	ContractVersion = "liki.agent-deployment.v1"
)

// AgentDeployment is the validated AgentDeployment artifact and its derived digest.
type AgentDeployment struct {
	SchemaVersion string             `json:"schema_version"`
	Metadata      DeploymentMetadata `json:"metadata"`
	Spec          DeploymentSpec     `json:"spec"`

	Digest string `json:"-"`
}

// DeploymentMetadata identifies the deployment artifact.
type DeploymentMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DeploymentSpec declares the deployment's MCP dependencies and Agent tree.
type DeploymentSpec struct {
	MCPServers []MCPServerDefinition `json:"mcpServers"`
	Agents     []AgentDefinition     `json:"agents"`
}

// MCPServerDefinition binds a logical MCP server to environment variables.
type MCPServerDefinition struct {
	Name        string `json:"name"`
	EndpointEnv string `json:"endpointEnv"`
	TokenEnv    string `json:"tokenEnv,omitempty"`
}

// AgentMode selects an ADK execution mode for an Agent.
type AgentMode string

// Supported Agent execution modes.
const (
	AgentModeChat       AgentMode = "chat"
	AgentModeTask       AgentMode = "task"
	AgentModeSingleTurn AgentMode = "single_turn"
)

// AgentReference is an outgoing delegation edge in the Agent tree.
type AgentReference struct {
	Name string `json:"name"`
}

// AgentDefinition is one deployed Agent: instruction, optional output schema,
// tool allowlist, and delegation edges.
type AgentDefinition struct {
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Description string           `json:"description"`
	Mode        AgentMode        `json:"mode"`
	SubAgents   []AgentReference `json:"sub_agents"`
	Instruction FileReference    `json:"instruction"`
	Output      OutputDefinition `json:"output"`
	Tools       ToolAllowlist    `json:"tools"`
	Skills      *SkillsBinding   `json:"skills,omitempty"`

	// Optional per-Agent model settings. They override the runtime-wide
	// environment values; zero values fall back to the runtime defaults.
	Model           string   `json:"model,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`

	// Runtime-only resolved values. They are never read from JSON.
	InstructionDigest string               `json:"-"`
	InstructionText   string               `json:"-"`
	SchemaDigest      string               `json:"-"`
	OutputSchema      *jsonschema.Schema   `json:"-"`
	ResolvedOutput    *jsonschema.Resolved `json:"-"`
	GenaiOutputSchema *genai.Schema        `json:"-"`
	RawOutputSchema   map[string]any       `json:"-"`
	Digest            string               `json:"-"`
}

// FileReference points at an external artifact relative to the deployment
// directory.
type FileReference struct {
	Path string `json:"path"`
}

// OutputDefinition declares an Agent's optional structured-output schema and
// the JSON Pointer selecting its user-facing text.
type OutputDefinition struct {
	Schema      FileReference `json:"schema"`
	TextPointer string        `json:"textPointer"`
}

// Structured reports whether the Agent declares a structured-output schema.
func (o OutputDefinition) Structured() bool {
	return o.Schema.Path != "" || o.TextPointer != ""
}

// EffectiveModel returns the Agent's model name, falling back to the
// runtime-wide model when the artifact does not override it.
func (a *AgentDefinition) EffectiveModel(runtime string) string {
	if a.Model != "" {
		return a.Model
	}
	return runtime
}

// EffectiveTemperature returns the Agent's model temperature, falling back to
// the runtime-wide temperature when the artifact does not override it.
func (a *AgentDefinition) EffectiveTemperature(runtime float32) float32 {
	if a.Temperature != nil {
		return float32(*a.Temperature)
	}
	return runtime
}

// EffectiveMaxOutputTokens returns the Agent's output bound, falling back to
// the runtime-wide bound when the artifact does not override it. Zero means
// the provider default is left unbounded.
func (a *AgentDefinition) EffectiveMaxOutputTokens(runtime int) int {
	if a.MaxOutputTokens > 0 {
		return a.MaxOutputTokens
	}
	return runtime
}

// ExposesModelText is the single policy for model-text visibility. A plain-text
// Agent may stream its model output; a structured Agent never exposes raw model
// text and only projects its validated user-facing answer. Protocol adapters
// must consult this function rather than re-deriving the rule.
func ExposesModelText(definition *AgentDefinition) bool {
	return definition != nil && !definition.Output.Structured()
}

// ToolAllowlist is an Agent's server-scoped allowlist of MCP tools.
type ToolAllowlist struct {
	Allow map[string][]string `json:"allow"`
}

// SkillsBinding binds an agent to a skilltoolset filesystem root (ADK skill
// layout: root contains skill directories, each with a SKILL.md whose
// frontmatter name matches the directory name).
type SkillsBinding struct {
	// Root is an absolute path inside the assembly image (/skills or
	// /expert-packs/liki-<name>/skills).
	Root string `json:"root"`
	// Preload is "" (frontmatter), "frontmatter", or "complete".
	Preload string `json:"preload,omitempty"`
}

// ToolReference names one allowlisted tool on one logical MCP server.
type ToolReference struct {
	Server string `json:"server"`
	Name   string `json:"name"`
}

// DefinitionRef is the deployment-level identity used by audit and protocols.
type DefinitionRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// ToolReferenceFor returns the unique server-scoped reference for an
// allowlisted tool, using deterministic server order for audit provenance.
func (a *AgentDefinition) ToolReferenceFor(toolName string) (ToolReference, bool) {
	servers := make([]string, 0, len(a.Tools.Allow))
	for server := range a.Tools.Allow {
		servers = append(servers, server)
	}
	slices.Sort(servers)
	for _, server := range servers {
		if slices.Contains(a.Tools.Allow[server], toolName) {
			return ToolReference{Server: server, Name: toolName}, true
		}
	}
	return ToolReference{}, false
}

// References returns the allowlist as a stable, server-scoped list.
func (t ToolAllowlist) References() []ToolReference {
	references := make([]ToolReference, 0)
	for server, tools := range t.Allow {
		for _, name := range tools {
			references = append(references, ToolReference{Server: server, Name: name})
		}
	}
	slices.SortFunc(references, func(a, b ToolReference) int {
		if compare := strings.Compare(a.Server, b.Server); compare != 0 {
			return compare
		}
		return strings.Compare(a.Name, b.Name)
	})
	return references
}
