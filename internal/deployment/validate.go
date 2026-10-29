package deployment

import (
	"errors"
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

// ValidateAgent rechecks one Agent's deployment-local invariants against the
// aggregate's declared MCP servers.
func (a *AgentDefinition) ValidateAgent(mcpServers map[string]struct{}) error {
	return a.validate(mcpServers)
}

func (d *AgentDeployment) validate() error {
	if d.SchemaVersion != ContractVersion {
		return fmt.Errorf("unsupported schema_version %q; want %q", d.SchemaVersion, ContractVersion)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("agent deployment metadata.name is required")
	}
	if strings.TrimSpace(d.Metadata.Version) == "" {
		return fmt.Errorf("agent deployment metadata.version is required")
	}
	mcpServers := make(map[string]struct{}, len(d.Spec.MCPServers))
	for index := range d.Spec.MCPServers {
		server := &d.Spec.MCPServers[index]
		if err := server.validate(); err != nil {
			return fmt.Errorf("mcpServers[%d]: %w", index, err)
		}
		if _, exists := mcpServers[server.Name]; exists {
			return fmt.Errorf("mcpServers[%d]: duplicate MCP server %q", index, server.Name)
		}
		mcpServers[server.Name] = struct{}{}
	}
	if len(d.Spec.Agents) == 0 {
		return fmt.Errorf("agent deployment must contain at least one agent")
	}

	names := make(map[string]struct{}, len(d.Spec.Agents))
	for index := range d.Spec.Agents {
		agent := &d.Spec.Agents[index]
		if err := agent.validate(mcpServers); err != nil {
			return fmt.Errorf("agent[%d]: %w", index, err)
		}
		if _, exists := names[agent.Name]; exists {
			return fmt.Errorf("duplicate agent name %q", agent.Name)
		}
		names[agent.Name] = struct{}{}
	}
	return d.validateAgentGraph(names)
}

func (s *MCPServerDefinition) validate() error {
	if strings.TrimSpace(s.Name) == "" || s.Name != strings.TrimSpace(s.Name) {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(s.EndpointEnv) == "" || s.EndpointEnv != strings.TrimSpace(s.EndpointEnv) {
		return fmt.Errorf("endpointEnv is required")
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
				return fmt.Errorf("agent %q sub_agents[%d]: name is required", agent.Name, index)
			}
			if _, exists := names[target]; !exists {
				return fmt.Errorf("agent %q references unknown sub-agent %q", agent.Name, target)
			}
			if target == agent.Name {
				return fmt.Errorf("agent %q cannot be its own sub-agent", agent.Name)
			}
			if _, exists := seen[target]; exists {
				return fmt.Errorf("agent %q has duplicate sub-agent %q", agent.Name, target)
			}
			if previous, exists := callers[target]; exists {
				return fmt.Errorf("agent %q has multiple parents (%q and %q); AgentDeployments must form a tree", target, previous, agent.Name)
			}
			callers[target] = agent.Name
			incoming[target]++
			seen[target] = struct{}{}
		}
	}

	roots := make([]string, 0, 1)
	for name, count := range incoming {
		if count == 0 {
			roots = append(roots, name)
		}
	}
	if len(roots) != 1 {
		return fmt.Errorf("deployment must have exactly one root Agent, got %d", len(roots))
	}
	root := roots[0]

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
			return fmt.Errorf("agent delegation graph contains a cycle involving %q", name)
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
			return fmt.Errorf("agent %q is not reachable from root %q", name, root)
		}
	}
	return nil
}

func (a *AgentDefinition) validate(mcpServers map[string]struct{}) error {
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(a.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if strings.TrimSpace(a.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if strings.TrimSpace(a.Instruction.Path) == "" {
		return fmt.Errorf("instruction.path is required")
	}
	if a.Temperature != nil && (*a.Temperature < 0 || *a.Temperature > 2) {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if a.MaxOutputTokens < 0 || a.MaxOutputTokens > math.MaxInt32 {
		return fmt.Errorf("maxOutputTokens must be between 0 and MaxInt32")
	}
	if a.Skills != nil {
		if strings.TrimSpace(a.Skills.Root) == "" {
			return fmt.Errorf("skills.root is required")
		}
		if !strings.HasPrefix(a.Skills.Root, "/") {
			return fmt.Errorf("skills.root %q must be an absolute path", a.Skills.Root)
		}
		switch a.Skills.Preload {
		case "", "frontmatter", "complete":
		default:
			return fmt.Errorf("skills.preload %q must be one of frontmatter, complete", a.Skills.Preload)
		}
		if err := ValidateSkillToolsetAllow(a.Tools.Allow[SkillToolsetServer]); err != nil {
			return fmt.Errorf("skills binding requires tools.allow[%s]: %w", SkillToolsetServer, err)
		}
	} else if _, has := a.Tools.Allow[SkillToolsetServer]; has {
		return fmt.Errorf("tools.allow[%s] requires a skills binding", SkillToolsetServer)
	}
	if a.Output.Structured() {
		if strings.TrimSpace(a.Output.Schema.Path) == "" {
			return fmt.Errorf("output.schema.path is required when structured output is enabled")
		}
		if a.Output.TextPointer == "/" {
			return fmt.Errorf("output.textPointer cannot select the object root")
		}
		if !strings.HasPrefix(a.Output.TextPointer, "/") {
			return fmt.Errorf("output.textPointer must be a JSON Pointer when structured output is enabled")
		}
	}
	switch a.Mode {
	case AgentModeChat, AgentModeTask, AgentModeSingleTurn:
	default:
		return fmt.Errorf("unsupported mode %q", a.Mode)
	}
	if a.Name == "user" {
		return fmt.Errorf(`name %q is reserved by ADK`, a.Name)
	}
	if strings.Contains(a.Name, ".") || strings.ContainsAny(a.Name, " \t\r\n") {
		return fmt.Errorf("name %q contains characters incompatible with ADK branch paths", a.Name)
	}
	seen := make(map[ToolReference]struct{})
	seenNames := make(map[string]string)
	for server, tools := range a.Tools.Allow {
		if server == SkillToolsetServer {
			if err := ValidateSkillToolsetAllow(tools); err != nil {
				return fmt.Errorf("tools.allow[%s]: %w", server, err)
			}
		} else if _, exists := mcpServers[server]; !exists {
			return fmt.Errorf("tools.allow references unknown MCP server %q", server)
		}
		for _, tool := range tools {
			if tool == "" || strings.TrimSpace(tool) != tool {
				return fmt.Errorf("tools.allow[%s] contains an empty tool name", server)
			}
			if len(tool) > 128 {
				return fmt.Errorf("tools.allow[%s] tool name exceeds MCP maximum length of 128", server)
			}
			for _, char := range tool {
				if char >= 'a' && char <= 'z' ||
					char >= 'A' && char <= 'Z' ||
					char >= '0' && char <= '9' ||
					char == '_' || char == '-' || char == '.' {
					continue
				}
				return fmt.Errorf("tools.allow[%s] tool name %q contains a character invalid for MCP", server, tool)
			}
			reference := ToolReference{Server: server, Name: tool}
			if _, exists := seen[reference]; exists {
				return fmt.Errorf("duplicate tool %q for MCP server %q", tool, server)
			}
			if previous, exists := seenNames[tool]; exists {
				return fmt.Errorf("tool %q is allowlisted by multiple MCP servers (%q and %q)", tool, previous, server)
			}
			seen[reference] = struct{}{}
			seenNames[tool] = server
		}
	}
	return nil
}

func validateOutputPointer(schema *jsonschema.Schema, pointer string) error {
	pointerValue, err := jsonpointer.Parse(pointer)
	if err != nil {
		return fmt.Errorf("parse RFC 6901 JSON Pointer: %w", err)
	}
	tokens := []string(pointerValue)
	current := schema
	for index, token := range tokens {
		if current == nil {
			return fmt.Errorf("token %d resolves to no schema", index)
		}
		switch {
		case current.Type == "array" || current.Items != nil || len(current.ItemsArray) != 0:
			if token != "-" && !isDecimal(token) {
				return fmt.Errorf("array token %q must be an index or '-'", token)
			}
			switch {
			case current.Items != nil:
				current = current.Items
			case token != "-" && len(current.ItemsArray) != 0:
				arrayIndex, _ := strconv.Atoi(token)
				if arrayIndex < 0 || arrayIndex >= len(current.ItemsArray) {
					return fmt.Errorf("array index %d is outside prefix items", arrayIndex)
				}
				current = current.ItemsArray[arrayIndex]
			default:
				return errors.New("array item schema is not statically known")
			}
		case current.Type == "object" || len(current.Properties) != 0:
			property, exists := current.Properties[token]
			if !exists {
				if current.AdditionalProperties != nil {
					current = current.AdditionalProperties
					continue
				}
				return fmt.Errorf("object token %q has no statically known property schema", token)
			}
			current = property
		default:
			return fmt.Errorf("token %q traverses a non-container schema", token)
		}
	}
	if current == nil || !selectsString(current) {
		return fmt.Errorf("pointer %q must select a string schema", pointer)
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
