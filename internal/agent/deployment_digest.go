package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type deploymentDigestInput struct {
	ManifestDigest string             `json:"manifestDigest"`
	Agents         []agentDigestInput `json:"agents"`
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
}

func agentDigestInputFrom(definition *AgentDefinition) agentDigestInput {
	references := definition.Tools.References()
	return agentDigestInput{
		Name:              definition.Name,
		Version:           definition.Version,
		Description:       definition.Description,
		Mode:              definition.Mode,
		SubAgents:         append([]AgentReference(nil), definition.SubAgents...),
		InstructionDigest: definition.InstructionDigest,
		SchemaDigest:      definition.SchemaDigest,
		TextPointer:       definition.Output.TextPointer,
		Tools:             references,
	}
}

func agentDigest(definition AgentDefinition) (string, error) {
	raw, err := json.Marshal(agentDigestInputFrom(&definition))
	if err != nil {
		return "", fmt.Errorf("marshal agent digest input: %w", err)
	}
	return sha256Hex(raw), nil
}

func deploymentDigest(manifest []byte, deployment *AgentDeployment) (string, error) {
	agents := make([]agentDigestInput, 0, len(deployment.Spec.Agents))
	for index := range deployment.Spec.Agents {
		agents = append(agents, agentDigestInputFrom(&deployment.Spec.Agents[index]))
	}
	raw, err := json.Marshal(deploymentDigestInput{
		ManifestDigest: sha256Hex(manifest),
		Agents:         agents,
	})
	if err != nil {
		return "", fmt.Errorf("marshal deployment digest input: %w", err)
	}
	return sha256Hex(raw), nil
}

// EntrypointDefinition returns the sole root of the ADK Agent tree.
func (d *AgentDeployment) EntrypointDefinition() (*AgentDefinition, error) {
	root, err := d.RootName()
	if err != nil {
		return nil, err
	}
	for index := range d.Spec.Agents {
		if d.Spec.Agents[index].Name == root {
			return &d.Spec.Agents[index], nil
		}
	}
	return nil, fmt.Errorf("root agent %q is not defined", root)
}

// RootName derives the entrypoint from the ADK Agent tree rather than from a
// duplicate deployment field.
func (d *AgentDeployment) RootName() (string, error) {
	incoming := make(map[string]int, len(d.Spec.Agents))
	for _, definition := range d.Spec.Agents {
		incoming[definition.Name] += 0
		for _, reference := range definition.SubAgents {
			incoming[reference.Name]++
		}
	}
	var roots []string
	for _, definition := range d.Spec.Agents {
		if incoming[definition.Name] == 0 {
			roots = append(roots, definition.Name)
		}
	}
	if len(roots) != 1 {
		return "", fmt.Errorf("deployment must have exactly one root Agent, got %d", len(roots))
	}
	return roots[0], nil
}

// Agent returns the AgentDefinition with the given name, if present.
func (d *AgentDeployment) Agent(name string) (*AgentDefinition, bool) {
	for index := range d.Spec.Agents {
		if d.Spec.Agents[index].Name == name {
			return &d.Spec.Agents[index], true
		}
	}
	return nil, false
}

// DefinitionRef returns the deployment-level identity used by audit and MCP.
func (d *AgentDeployment) DefinitionRef() DefinitionRef {
	return DefinitionRef{
		Name:    d.Metadata.Name,
		Version: d.Metadata.Version,
		Digest:  d.Digest,
	}
}
