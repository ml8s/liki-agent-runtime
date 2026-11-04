package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func nonNilSlice[T any](value []T) []T {
	if value == nil {
		return []T{}
	}
	return value
}

type deploymentDigestInput struct {
	SchemaVersion string                `json:"schemaVersion"`
	Deployment    DeploymentMetadata    `json:"deployment"`
	MCPServers    []MCPServerDefinition `json:"mcpServers"`
	Agents        []agentDigestInput    `json:"agents"`
}

type agentDigestInput struct {
	Name              string           `json:"name"`
	Version           string           `json:"version"`
	Description       string           `json:"description"`
	Mode              AgentMode        `json:"mode"`
	SubAgents         []AgentReference `json:"subAgents"`
	InstructionDigest string           `json:"instructionDigest"`
	SchemaDigest      string           `json:"schemaDigest,omitempty"`
	TextPointer       string           `json:"textPointer,omitempty"`
	Tools             []ToolReference  `json:"tools"`
	Model             string           `json:"model,omitempty"`
	Temperature       *float64         `json:"temperature,omitempty"`
	MaxOutputTokens   int              `json:"maxOutputTokens,omitempty"`
	Skills            *SkillsBinding   `json:"skills,omitempty"`
}

func agentDigestInputFrom(definition *ResolvedAgentDefinition) agentDigestInput {
	references := definition.Tools.References()
	return agentDigestInput{
		Name:              definition.Name,
		Version:           definition.Version,
		Description:       definition.Description,
		Mode:              definition.Mode,
		SubAgents:         nonNilSlice(definition.SubAgents),
		InstructionDigest: definition.InstructionDigest,
		SchemaDigest:      definition.SchemaDigest,
		TextPointer:       definition.Output.TextPointer,
		Tools:             references,
		Model:             definition.Model,
		Temperature:       definition.Temperature,
		MaxOutputTokens:   definition.MaxOutputTokens,
		Skills:            definition.Skills,
	}
}

func agentDigest(definition ResolvedAgentDefinition) (string, error) {
	raw, err := json.Marshal(agentDigestInputFrom(&definition))
	if err != nil {
		return "", artifactErrorWrap(CodeDeploymentIdentityInvalid, "marshal agent digest input", err)
	}
	return sha256Hex(raw), nil
}

func deploymentDigest(deployment *AgentDeployment) (string, error) {
	mcpServers := nonNilSlice(deployment.Spec.MCPServers)
	agents := make([]agentDigestInput, 0, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		agents = append(agents, agentDigestInputFrom(&deployment.Spec.Agents[index]))
	}
	raw, err := json.Marshal(deploymentDigestInput{
		SchemaVersion: deployment.SchemaVersion,
		Deployment:    deployment.Metadata,
		MCPServers:    mcpServers,
		Agents:        agents,
	})
	if err != nil {
		return "", artifactErrorWrap(CodeDeploymentIdentityInvalid, "marshal deployment digest input", err)
	}
	return sha256Hex(raw), nil
}

// Clone returns an aggregate-owned copy. Callers may mutate the copy without
// invalidating a runtime that continues to use the source snapshot. JSON
// schema pointers are immutable compiled artifacts and remain shared by design.
func (d *AgentDeployment) Clone() (*AgentDeployment, error) {
	if d == nil {
		return nil, nil
	}
	next := *d
	next.manifest = append([]byte(nil), d.manifest...)
	next.agents = nil
	next.Spec.MCPServers = nonNilSlice(d.Spec.MCPServers)
	next.Spec.Agents = make([]ResolvedAgentDefinition, len(d.Spec.Agents))
	d.buildAgentIndex()
	for index, agent := range d.Spec.Agents {
		agent.SubAgents = nonNilSlice(agent.SubAgents)
		if agent.Tools.Allow != nil {
			agent.Tools.Allow = make(map[string][]string, len(agent.Tools.Allow))
			for server, tools := range d.Spec.Agents[index].Tools.Allow {
				agent.Tools.Allow[server] = append([]string(nil), tools...)
			}
		}
		if agent.Skills != nil {
			skills := *agent.Skills
			agent.Skills = &skills
		}
		if agent.Temperature != nil {
			temperature := *agent.Temperature
			agent.Temperature = &temperature
		}
		agent.toolRefs = nil
		agent.buildToolIndex()
		if agent.RawOutputSchema != nil {
			rawSchema, err := deepCopyJSONValue(agent.RawOutputSchema)
			if err != nil {
				return nil, err
			}
			copied, ok := rawSchema.(map[string]any)
			if !ok {
				return nil, artifactError(CodeDeploymentOutputSchemaInvalid, "agent %q raw output schema is not an object", agent.Name)
			}
			agent.RawOutputSchema = copied
		}
		next.Spec.Agents[index] = agent
	}
	next.buildAgentIndex()
	return &next, nil
}

// ValidateIntegrity recomputes derived identity from loaded content. Runtime
// constructors call it after taking an aggregate-owned copy so accidental
// caller mutation fails closed instead of changing execution behavior silently.
func (d *AgentDeployment) ValidateIntegrity() error {
	agents := make([]agentDigestInput, 0, len(d.Spec.Agents))
	for index := range d.Spec.Agents {
		agents = append(agents, agentDigestInputFrom(&d.Spec.Agents[index]))
	}
	raw, err := json.Marshal(deploymentDigestInput{
		SchemaVersion: d.SchemaVersion,
		Deployment:    d.Metadata,
		MCPServers:    d.Spec.MCPServers,
		Agents:        agents,
	})
	if err != nil {
		return artifactErrorWrap(CodeDeploymentIdentityInvalid, "validate deployment digest", err)
	}
	semantic := sha256Hex(raw)
	if d.Digest != semantic {
		return artifactError(CodeDeploymentIntegrityInvalid, "deployment digest mismatch: stored=%s recomputed=%s", d.Digest, semantic)
	}
	if d.ManifestDigest != sha256Hex(d.manifest) {
		return artifactError(CodeDeploymentIntegrityInvalid, "manifest digest mismatch")
	}
	return nil
}

func deepCopyJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			copied, err := deepCopyJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[key] = copied
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			copied, err := deepCopyJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[index] = copied
		}
		return result, nil
	default:
		return typed, nil
	}
}

// EntrypointDefinition returns the sole root of the ADK Agent tree.
func (d *AgentDeployment) EntrypointDefinition() (*ResolvedAgentDefinition, error) {
	root, err := rootAgent(d.Spec.Agents)
	if err != nil {
		return nil, err
	}
	for index := range d.Spec.Agents {
		if d.Spec.Agents[index].Name == root {
			return &d.Spec.Agents[index], nil
		}
	}
	return nil, validationError(CodeDeploymentGraphInvalid, "root agent %q is not defined", root)
}

// buildAgentIndex materializes the immutable name lookup. Aggregates are
// immutable after loading or cloning; hand-built tests may call Validate,
// which also rebuilds after graph invariants pass.
func (d *AgentDeployment) buildAgentIndex() {
	if d.agents == nil {
		d.agents = make(map[string]*ResolvedAgentDefinition, len(d.Spec.Agents))
	}
	clear(d.agents)
	for index := range d.Spec.Agents {
		definition := &d.Spec.Agents[index]
		d.agents[definition.Name] = definition
	}
}

// Agent returns the resolved AgentDefinition with the given name, if present.
func (d *AgentDeployment) Agent(name string) (*ResolvedAgentDefinition, bool) {
	d.buildAgentIndex()
	if definition, ok := d.agents[name]; ok {
		return definition, true
	}
	return nil, false
}

// DeploymentRef returns the deployment-level identity used by audit and MCP.
func (d *AgentDeployment) DeploymentRef() DeploymentRef {
	return DeploymentRef{
		Name:    d.Metadata.Name,
		Version: d.Metadata.Version,
		Digest:  d.Digest,
	}
}

// AgentDeclarations returns public declaration copies for configuration-level
// validation. Loaded instruction/schema state remains aggregate-owned.
func (d *AgentDeployment) AgentDeclarations() []AgentDefinition {
	declarations := make([]AgentDefinition, 0, len(d.Spec.Agents))
	for index := range d.Spec.Agents {
		declarations = append(declarations, cloneAgentDeclaration(&d.Spec.Agents[index].AgentDefinition))
	}
	return declarations
}
