package deployment

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	jsonpointer "github.com/qri-io/jsonpointer"
)

// Validate rechecks an in-memory deployment, including its derived graph
// invariants. This is useful after a caller composes a test or migration
// artifact without changing immutable files.
func (d *AgentDeployment) Validate() error {
	return d.validate()
}

func (d *AgentDeployment) validate() error {
	if d.SchemaVersion != ContractVersion {
		return validationError(CodeDeploymentSchemaVersionInvalid, "unsupported schema_version %q; want %q", d.SchemaVersion, ContractVersion)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return validationError(CodeDeploymentMetadataInvalid, "agent deployment metadata.name is required")
	}
	if strings.TrimSpace(d.Metadata.Version) == "" {
		return validationError(CodeDeploymentMetadataInvalid, "agent deployment metadata.version is required")
	}
	mcpServers := make(map[string]struct{}, len(d.Spec.MCPServers))
	for index := range d.Spec.MCPServers {
		server := &d.Spec.MCPServers[index]
		if err := server.validate(); err != nil {
			return validationErrorWrap(CodeDeploymentMCPServerInvalid, fmt.Sprintf("mcpServers[%d]", index), err)
		}
		if _, exists := mcpServers[server.Name]; exists {
			return validationError(CodeDeploymentMCPServerDuplicate, "mcpServers[%d]: duplicate MCP server %q", index, server.Name)
		}
		mcpServers[server.Name] = struct{}{}
	}
	if len(d.Spec.Agents) == 0 {
		return validationError(CodeDeploymentAgentMissing, "agent deployment must contain at least one agent")
	}

	names := make(map[string]struct{}, len(d.Spec.Agents))
	for index := range d.Spec.Agents {
		agent := &d.Spec.Agents[index]
		if err := agent.validate(mcpServers); err != nil {
			return validationErrorWrap(CodeDeploymentAgentInvalid, fmt.Sprintf("agent[%d]", index), err)
		}
		if _, exists := names[agent.Name]; exists {
			return validationError(CodeDeploymentAgentDuplicate, "duplicate agent name %q", agent.Name)
		}
		names[agent.Name] = struct{}{}
		agent.buildToolIndex()
	}
	if err := d.validateAgentGraph(names); err != nil {
		return err
	}
	d.buildAgentIndex()
	return nil
}

func (s *MCPServerDefinition) validate() error {
	if strings.TrimSpace(s.Name) == "" || s.Name != strings.TrimSpace(s.Name) {
		return validationError(CodeDeploymentMCPServerInvalid, "name is required")
	}
	if strings.TrimSpace(s.EndpointEnv) == "" || s.EndpointEnv != strings.TrimSpace(s.EndpointEnv) {
		return validationError(CodeDeploymentMCPServerInvalid, "endpointEnv is required")
	}
	return nil
}

func (d *AgentDeployment) validateAgentGraph(names map[string]struct{}) error {
	callers := make(map[string]string, len(d.Spec.Agents))
	incoming := make(map[string]int, len(d.Spec.Agents))
	for name := range names {
		incoming[name] = 0
	}
	for _, agent := range d.Spec.Agents {
		seen := make(map[string]struct{}, len(agent.SubAgents))
		for index, reference := range agent.SubAgents {
			target := strings.TrimSpace(reference.Name)
			if target == "" {
				return validationError(CodeDeploymentGraphInvalid, "agent %q sub_agents[%d]: name is required", agent.Name, index)
			}
			if _, exists := names[target]; !exists {
				return validationError(CodeDeploymentGraphInvalid, "agent %q references unknown sub-agent %q", agent.Name, target)
			}
			if target == agent.Name {
				return validationError(CodeDeploymentGraphInvalid, "agent %q cannot be its own sub-agent", agent.Name)
			}
			if _, exists := seen[target]; exists {
				return validationError(CodeDeploymentGraphInvalid, "agent %q has duplicate sub-agent %q", agent.Name, target)
			}
			if previous, exists := callers[target]; exists {
				return validationError(CodeDeploymentGraphInvalid, "agent %q has multiple parents (%q and %q); AgentDeployments must form a tree", target, previous, agent.Name)
			}
			callers[target] = agent.Name
			incoming[target]++
			seen[target] = struct{}{}
		}
	}

	root, err := rootAgent(d.Spec.Agents)
	if err != nil {
		return err
	}

	visiting := make(map[string]struct{})
	visited := make(map[string]struct{})
	definitions := make(map[string]*ResolvedAgentDefinition, len(d.Spec.Agents))
	for index := range d.Spec.Agents {
		definitions[d.Spec.Agents[index].Name] = &d.Spec.Agents[index]
	}

	var visit func(string) error
	visit = func(name string) error {
		switch _, done := visited[name]; {
		case done:
			return nil
		case visiting[name] != struct{}{}:
			return validationError(CodeDeploymentGraphInvalid, "agent delegation graph contains a cycle involving %q", name)
		}
		visiting[name] = struct{}{}
		for _, reference := range definitions[name].SubAgents {
			if err := visit(reference.Name); err != nil {
				return err
			}
		}
		delete(visiting, name)
		visited[name] = struct{}{}
		return nil
	}
	for _, agent := range d.Spec.Agents {
		if err := visit(agent.Name); err != nil {
			return err
		}
	}

	reachable := make(map[string]struct{})
	var walk func(string)
	walk = func(name string) {
		if _, done := reachable[name]; done {
			return
		}
		reachable[name] = struct{}{}
		for _, reference := range definitions[name].SubAgents {
			walk(reference.Name)
		}
	}
	walk(root)
	for name := range names {
		if _, ok := reachable[name]; !ok {
			return validationError(CodeDeploymentGraphInvalid, "agent %q is not reachable from root %q", name, root)
		}
	}
	return nil
}

func (a *AgentDefinition) validate(mcpServers map[string]struct{}) error {
	if strings.TrimSpace(a.Name) == "" {
		return validationError(CodeDeploymentAgentInvalid, "name is required")
	}
	if strings.TrimSpace(a.Version) == "" {
		return validationError(CodeDeploymentAgentInvalid, "version is required")
	}
	if strings.TrimSpace(a.Description) == "" {
		return validationError(CodeDeploymentAgentInvalid, "description is required")
	}
	if strings.TrimSpace(a.Instruction.Path) == "" {
		return validationError(CodeDeploymentAgentInvalid, "instruction.path is required")
	}
	if a.Temperature != nil && (*a.Temperature < 0 || *a.Temperature > 2) {
		return validationError(CodeDeploymentAgentInvalid, "temperature must be between 0 and 2")
	}
	if a.MaxOutputTokens < 0 || a.MaxOutputTokens > math.MaxInt32 {
		return validationError(CodeDeploymentAgentInvalid, "maxOutputTokens must be between 0 and MaxInt32")
	}
	if a.Skills != nil {
		if strings.TrimSpace(a.Skills.Root) == "" {
			return validationError(CodeDeploymentSkillsBindingInvalid, "skills.root is required")
		}
		if !strings.HasPrefix(a.Skills.Root, "/") {
			return validationError(CodeDeploymentSkillsBindingInvalid, "skills.root %q must be an absolute path", a.Skills.Root)
		}
		switch a.Skills.Preload {
		case "", "frontmatter", "complete":
		default:
			return validationError(CodeDeploymentSkillsBindingInvalid, "skills.preload %q must be one of frontmatter, complete", a.Skills.Preload)
		}
		if err := ValidateSkillToolsetAllow(a.Tools.Allow[SkillToolsetServer]); err != nil {
			return validationErrorWrap(CodeDeploymentSkillsBindingInvalid, fmt.Sprintf("skills binding requires tools.allow[%s]", SkillToolsetServer), err)
		}
	} else if _, has := a.Tools.Allow[SkillToolsetServer]; has {
		return validationError(CodeDeploymentSkillsBindingInvalid, "tools.allow[%s] requires a skills binding", SkillToolsetServer)
	}
	if a.Output.Structured() {
		if strings.TrimSpace(a.Output.Schema.Path) == "" {
			return validationError(CodeDeploymentOutputSchemaInvalid, "output.schema.path is required when structured output is enabled")
		}
		if a.Output.TextPointer == "/" {
			return validationError(CodeDeploymentOutputPointerInvalid, "output.textPointer cannot select the object root")
		}
		if !strings.HasPrefix(a.Output.TextPointer, "/") {
			return validationError(CodeDeploymentOutputPointerInvalid, "output.textPointer must be a JSON Pointer when structured output is enabled")
		}
	}
	switch a.Mode {
	case AgentModeChat, AgentModeTask, AgentModeSingleTurn:
	default:
		return validationError(CodeDeploymentAgentInvalid, "unsupported mode %q", a.Mode)
	}
	if a.Name == "user" {
		return validationError(CodeDeploymentAgentInvalid, "name %q is reserved by ADK", a.Name)
	}
	if strings.Contains(a.Name, ".") || strings.ContainsAny(a.Name, " \t\r\n") {
		return validationError(CodeDeploymentAgentInvalid, "name %q contains characters incompatible with ADK branch paths", a.Name)
	}
	seen := make(map[ToolReference]struct{})
	seenNames := make(map[string]string)
	for server, tools := range a.Tools.Allow {
		if server == SkillToolsetServer {
			if err := ValidateSkillToolsetAllow(tools); err != nil {
				return validationErrorWrap(CodeDeploymentToolAllowlistInvalid, fmt.Sprintf("tools.allow[%s]", server), err)
			}
		} else if _, exists := mcpServers[server]; !exists {
			return validationError(CodeDeploymentToolAllowlistInvalid, "tools.allow references unknown MCP server %q", server)
		}
		for _, tool := range tools {
			if tool == "" || strings.TrimSpace(tool) != tool {
				return validationError(CodeDeploymentToolAllowlistInvalid, "tools.allow[%s] contains an empty tool name", server)
			}
			if len(tool) > 128 {
				return validationError(CodeDeploymentToolAllowlistInvalid, "tools.allow[%s] tool name exceeds MCP maximum length of 128", server)
			}
			for _, char := range tool {
				if char >= 'a' && char <= 'z' ||
					char >= 'A' && char <= 'Z' ||
					char >= '0' && char <= '9' ||
					char == '_' || char == '-' || char == '.' {
					continue
				}
				return validationError(CodeDeploymentToolAllowlistInvalid, "tools.allow[%s] tool name %q contains a character invalid for MCP", server, tool)
			}
			reference := ToolReference{Server: server, Name: tool}
			if _, exists := seen[reference]; exists {
				return validationError(CodeDeploymentToolAllowlistInvalid, "duplicate tool %q for MCP server %q", tool, server)
			}
			if previous, exists := seenNames[tool]; exists {
				return validationError(CodeDeploymentToolAllowlistInvalid, "tool %q is allowlisted by multiple MCP servers (%q and %q)", tool, previous, server)
			}
			seen[reference] = struct{}{}
			seenNames[tool] = server
		}
	}
	return nil
}

// rootAgent is the single entrypoint invariant shared by validation and
// aggregate lookup. Full graph validation still owns cycle and reachability.
func rootAgent(definitions []ResolvedAgentDefinition) (string, error) {
	incoming := make(map[string]int, len(definitions))
	for _, definition := range definitions {
		incoming[definition.Name] += 0
		for _, reference := range definition.SubAgents {
			incoming[reference.Name]++
		}
	}
	roots := make([]string, 0, 1)
	for _, definition := range definitions {
		if incoming[definition.Name] == 0 {
			roots = append(roots, definition.Name)
		}
	}
	if len(roots) != 1 {
		return "", validationError(CodeDeploymentGraphInvalid, "deployment must have exactly one root Agent, got %d", len(roots))
	}
	return roots[0], nil
}

func validateOutputPointer(schema *jsonschema.Schema, pointer string) error {
	pointerValue, err := jsonpointer.Parse(pointer)
	if err != nil {
		return validationErrorWrap(CodeDeploymentOutputPointerInvalid, "parse RFC 6901 JSON Pointer", err)
	}
	tokens := []string(pointerValue)
	current := schema
	for index, token := range tokens {
		if current == nil {
			return validationError(CodeDeploymentOutputPointerInvalid, "token %d resolves to no schema", index)
		}
		switch {
		case current.Type == "array" || current.Items != nil || len(current.ItemsArray) != 0:
			if token != "-" && !isDecimal(token) {
				return validationError(CodeDeploymentOutputPointerInvalid, "array token %q must be an index or '-'", token)
			}
			switch {
			case current.Items != nil:
				current = current.Items
			case token != "-" && len(current.ItemsArray) != 0:
				arrayIndex, _ := strconv.Atoi(token)
				if arrayIndex < 0 || arrayIndex >= len(current.ItemsArray) {
					return validationError(CodeDeploymentOutputPointerInvalid, "array index %d is outside prefix items", arrayIndex)
				}
				current = current.ItemsArray[arrayIndex]
			default:
				return validationError(CodeDeploymentOutputPointerInvalid, "array item schema is not statically known")
			}
		case current.Type == "object" || len(current.Properties) != 0:
			property, exists := current.Properties[token]
			if !exists {
				if current.AdditionalProperties != nil {
					current = current.AdditionalProperties
					continue
				}
				return validationError(CodeDeploymentOutputPointerInvalid, "object token %q has no statically known property schema", token)
			}
			current = property
		default:
			return validationError(CodeDeploymentOutputPointerInvalid, "token %q traverses a non-container schema", token)
		}
	}
	if current == nil || !selectsString(current) {
		return validationError(CodeDeploymentOutputPointerInvalid, "pointer %q must select a string schema", pointer)
	}
	return nil
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func selectsString(schema *jsonschema.Schema) bool {
	if schema == nil {
		return false
	}
	if schema.Type == "string" {
		return true
	}
	if schema.Type != "" || len(schema.Enum) == 0 {
		return false
	}
	for _, value := range schema.Enum {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}

// ToolReferenceFor returns the logical MCP server binding for a tool name.
// AgentDeployment validation rejects duplicate tool names across servers for one
// Agent, making this lookup deterministic after artifact validation.
