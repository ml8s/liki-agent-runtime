// Package deployment owns the external AgentDeployment aggregate.
package deployment

import (
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

const (
	maxManifestBytes       = 2 << 20
	maxTextArtifactBytes   = 2 << 20
	maxSchemaArtifactBytes = 4 << 20

	// ContractVersion identifies the AgentDeployment JSON shape. Runtime
	// releases reject artifacts written for an incompatible contract.
	ContractVersion = "agent.deployment.v1"
)

// AgentDeployment is the validated AgentDeployment artifact and its derived digest.
type AgentDeployment struct {
	SchemaVersion string             `json:"schema_version"`
	Metadata      DeploymentMetadata `json:"metadata"`
	Spec          DeploymentSpec     `json:"spec"`

	// Digest is the canonical semantic identity of all behavior-bearing
	// deployment state. It intentionally excludes artifact formatting.
	Digest string `json:"-"`
	// ManifestDigest identifies the exact source manifest bytes and is kept
	// separate from behavior identity so provenance and semantics do not share
	// one overloaded concept.
	ManifestDigest string `json:"-"`

	// manifest retains the exact source bytes solely for integrity checks.
	manifest []byte `json:"-"`

	// agents is an immutable lookup built after aggregate validation/loading.
	agents map[string]*ResolvedAgentDefinition `json:"-"`
}

// DeploymentMetadata identifies the deployment artifact.
type DeploymentMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DeploymentSpec declares the deployment's MCP dependencies and Agent tree.
type DeploymentSpec struct {
	MCPServers []MCPServerDefinition     `json:"mcpServers"`
	Agents     []ResolvedAgentDefinition `json:"agents"`
}

// MCPServerDefinition binds a logical MCP server to environment variables.
type MCPServerDefinition struct {
	Name        string `json:"name"`
	EndpointEnv string `json:"endpointEnv"`
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

// AgentDefinition is the external artifact declaration for one Agent. It has
// no loaded instruction text, schema state, digests, or provider mappings.
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

	// toolRefs is an immutable lookup built from a validated allowlist.
	toolRefs map[string]ToolReference `json:"-"`
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
	// /expert-packs/<name>/skills).
	Root string `json:"root"`
	// Preload is "" (frontmatter), "frontmatter", or "complete".
	Preload string `json:"preload,omitempty"`
}

// ToolReference names one allowlisted tool on one logical MCP server.
type ToolReference struct {
	Server string `json:"server"`
	Name   string `json:"name"`
}

// DeploymentRef is the deployment-level identity used by audit and protocols.
// AgentDefinition identity is carried separately by each agent's
// Name/Version/Digest fields.
type DeploymentRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// DeploymentSource is the runtime's aggregate boundary. Implementations return
// an aggregate-owned clone so caller-owned declarations cannot change after
// runtime construction.
type DeploymentSource interface {
	View() DeploymentView
	Clone() (*AgentDeployment, error)
	AgentDeclarations() []AgentDefinition
}

var _ DeploymentSource = (*AgentDeployment)(nil)

// ToolReferenceFor returns the unique server-scoped reference for an
// allowlisted tool.
func (a *AgentDefinition) ToolReferenceFor(toolName string) (ToolReference, bool) {
	a.buildToolIndex()
	reference, ok := a.toolRefs[toolName]
	return reference, ok
}

func (a *AgentDefinition) buildToolIndex() {
	if a.toolRefs == nil {
		a.toolRefs = make(map[string]ToolReference, len(a.Tools.Allow))
	}
	clear(a.toolRefs)
	for server, tools := range a.Tools.Allow {
		for _, name := range tools {
			a.toolRefs[name] = ToolReference{Server: server, Name: name}
		}
	}
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

// ResolvedAgentDefinition is an AgentDefinition after the deployment loader has
// loaded external artifacts and computed immutable identity. It contains
// runtime-neutral resolved state; provider mappings belong to the runtime view.
type ResolvedAgentDefinition struct {
	AgentDefinition

	InstructionText   string               `json:"-"`
	InstructionDigest string               `json:"-"`
	SchemaDigest      string               `json:"-"`
	OutputSchema      *jsonschema.Schema   `json:"-"`
	ResolvedOutput    *jsonschema.Resolved `json:"-"`
	Digest            string               `json:"-"`

	// RawOutputSchema is the loaded external schema in its neutral resolved
	// representation. Provider mappings are derived by the execution runtime.
	RawOutputSchema map[string]any `json:"-"`
}
