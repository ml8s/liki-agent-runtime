// Package agent owns the single ADK execution graph, protocol adaptation, and
// durable audit evidence. External AgentDeployment artifacts are the only
// attachment point for domain prompts, output schemas, tool allowlists, and
// Agent identity.
package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	jsonpointer "github.com/qri-io/jsonpointer"
	"google.golang.org/genai"
)

const (
	maxManifestBytes       = 2 << 20
	maxTextArtifactBytes   = 2 << 20
	maxSchemaArtifactBytes = 4 << 20
)

// Deployment is the validated AgentDeployment artifact and its derived digest.
type Deployment struct {
	Metadata DeploymentMetadata `json:"metadata"`
	Spec     DeploymentSpec     `json:"spec"`

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

type definitionLoader struct {
	root string
}

// LoadAgentDeployment validates an AgentDeployment artifact, resolves its
// external artifacts, and computes its content digest.
func LoadAgentDeployment(path string) (*Deployment, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("agent deployment file is required")
	}
	root, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("resolve agent deployment directory: %w", err)
	}
	loader := &definitionLoader{root: root}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent deployment: %w", err)
	}
	if len(raw) > maxManifestBytes {
		return nil, fmt.Errorf("agent deployment manifest exceeds %d bytes", maxManifestBytes)
	}
	deployment, err := decodeAgentDeployment(raw)
	if err != nil {
		return nil, err
	}
	if err := deployment.validate(); err != nil {
		return nil, err
	}
	if err := loader.loadAgents(deployment); err != nil {
		return nil, err
	}
	if deployment.Digest, err = deploymentDigest(raw, deployment); err != nil {
		return nil, fmt.Errorf("deployment digest: %w", err)
	}
	return deployment, nil
}

// Validate rechecks an in-memory deployment, including its derived graph
// invariants. This is useful after a caller composes a test or migration
// artifact without changing immutable files.
func (d *Deployment) Validate() error {
	return d.validate()
}

func decodeAgentDeployment(raw []byte) (*Deployment, error) {
	var deployment Deployment
	if err := decodeStrictJSON(raw, &deployment); err != nil {
		return nil, fmt.Errorf("decode agent deployment: %w", err)
	}
	return &deployment, nil
}

// decodeStrictJSON rejects duplicate object keys, trailing JSON values, and
// unknown typed fields. Artifact digests must not make ambiguous JSON portable.
func decodeStrictJSON(raw []byte, target any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON document: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON document contains trailing values")
		}
		return fmt.Errorf("decode JSON trailing input: %w", err)
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := rejectDuplicateKeysInValue(decoder, "$"); err != nil {
		return fmt.Errorf("JSON object key conflict: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON input: %w", err)
	}
	return nil
}

func rejectDuplicateKeysInValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read JSON token: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("read object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("expected object key at %s", path)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate key %q at %s", key, path)
			}
			seen[key] = struct{}{}
			if err := rejectDuplicateKeysInValue(decoder, path+"."+key); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			if err := rejectDuplicateKeysInValue(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
			index++
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
}

func (d *Deployment) validate() error {
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
	if s.TokenEnv != "" && s.TokenEnv != strings.TrimSpace(s.TokenEnv) {
		return fmt.Errorf("tokenEnv cannot contain padding whitespace")
	}
	return nil
}

func (d *Deployment) validateAgentGraph(names map[string]struct{}) error {
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
	definitions := make(map[string]*AgentDefinition, len(d.Spec.Agents))
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
	if a.MaxOutputTokens < 0 {
		return fmt.Errorf("maxOutputTokens must be a non-negative integer")
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
		if err := validateSkillToolsetAllow(a.Tools.Allow[skillToolsetServer]); err != nil {
			return fmt.Errorf("skills binding requires tools.allow[%s]: %w", skillToolsetServer, err)
		}
	} else if _, has := a.Tools.Allow[skillToolsetServer]; has {
		return fmt.Errorf("tools.allow[%s] requires a skills binding", skillToolsetServer)
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
		if server == skillToolsetServer {
			if err := validateSkillToolsetAllow(tools); err != nil {
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
// Deployment validation rejects duplicate tool names across servers for one
// Agent, making this lookup deterministic after artifact validation.
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

func (l *definitionLoader) loadAgents(deployment *Deployment) error {
	for index := range deployment.Spec.Agents {
		agent := &deployment.Spec.Agents[index]
		instructionRaw, err := l.readFile(agent.Instruction.Path)
		if err != nil {
			return fmt.Errorf("agent %q instruction: %w", agent.Name, err)
		}
		if len(instructionRaw) > maxTextArtifactBytes {
			return fmt.Errorf("agent %q instruction exceeds %d bytes", agent.Name, maxTextArtifactBytes)
		}
		instruction := string(instructionRaw)
		if strings.TrimSpace(instruction) == "" {
			return fmt.Errorf("agent %q instruction is empty", agent.Name)
		}
		agent.InstructionText = instruction
		agent.InstructionDigest = sha256Hex([]byte(instruction))
		if agent.Output.Structured() {
			schemaRaw, err := l.readFile(agent.Output.Schema.Path)
			if err != nil {
				return fmt.Errorf("agent %q output schema: %w", agent.Name, err)
			}
			if len(schemaRaw) > maxSchemaArtifactBytes {
				return fmt.Errorf("agent %q output schema exceeds %d bytes", agent.Name, maxSchemaArtifactBytes)
			}
			var outputSchema jsonschema.Schema
			if err := decodeStrictJSON(schemaRaw, &outputSchema); err != nil {
				return fmt.Errorf("agent %q decode output schema: %w", agent.Name, err)
			}
			resolved, err := outputSchema.Resolve(nil)
			if err != nil {
				return fmt.Errorf("agent %q resolve output schema: %w", agent.Name, err)
			}
			genaiSchema, err := convertJSONSchemaToGenai(&outputSchema)
			if err != nil {
				return fmt.Errorf("agent %q output schema: %w", agent.Name, err)
			}
			if err := validateOutputPointer(&outputSchema, agent.Output.TextPointer); err != nil {
				return fmt.Errorf("agent %q output.textPointer: %w", agent.Name, err)
			}
			if genaiSchema.Type != genai.TypeObject {
				return fmt.Errorf("agent %q output schema root must be an object", agent.Name)
			}
			agent.OutputSchema = &outputSchema
			agent.ResolvedOutput = resolved
			agent.SchemaDigest = sha256Hex(schemaRaw)
			agent.GenaiOutputSchema = genaiSchema
			var rawOutputSchema map[string]any
			if err := decodeStrictJSON(schemaRaw, &rawOutputSchema); err != nil {
				return fmt.Errorf("agent %q decode raw output schema: %w", agent.Name, err)
			}
			agent.RawOutputSchema = rawOutputSchema
		}
		agent.Digest, err = agentDigest(*agent)
		if err != nil {
			return fmt.Errorf("agent %q digest: %w", agent.Name, err)
		}
	}
	return nil
}

func (l *definitionLoader) readFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(l.root)
	if err != nil {
		return nil, fmt.Errorf("open agent deployment directory: %w", err)
	}
	data, err := root.ReadFile(path)
	if err != nil {
		closeErr := root.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("read %q (close failed: %w): %w", path, closeErr, err)
		}
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, fmt.Errorf("close agent deployment directory: %w", err)
	}
	return data, nil
}

func convertJSONSchemaToGenai(schema *jsonschema.Schema) (*genai.Schema, error) {
	if schema == nil {
		return nil, errors.New("schema is required")
	}
	if schema.Ref != "" || len(schema.AllOf) != 0 || len(schema.OneOf) != 0 || len(schema.AnyOf) != 0 || schema.Not != nil {
		return nil, errors.New("unsupported schema composition")
	}
	if len(schema.Types) != 0 {
		return nil, errors.New("schema type arrays are unsupported")
	}
	result := &genai.Schema{
		Description: schema.Description,
		Enum:        make([]string, 0, len(schema.Enum)),
		Format:      schema.Format,
		Title:       schema.Title,
	}
	for _, value := range schema.Enum {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode enum value: %w", err)
		}
		var text string
		if err := json.Unmarshal(encoded, &text); err != nil {
			return nil, errors.New("only string enums are supported")
		}
		result.Enum = append(result.Enum, text)
	}
	if schema.Type != "" {
		genaiType, err := genaiType(schema.Type)
		if err != nil {
			return nil, err
		}
		result.Type = genaiType
	}
	copyInt64 := func(value *int) (*int64, error) {
		if value == nil {
			return nil, nil
		}
		converted := int64(*value)
		return &converted, nil
	}
	copyFloat64 := func(value *float64) (*float64, error) {
		if value == nil {
			return nil, nil
		}
		converted := *value
		return &converted, nil
	}
	var err error
	if result.MinItems, err = copyInt64(schema.MinItems); err != nil {
		return nil, err
	}
	if result.MaxItems, err = copyInt64(schema.MaxItems); err != nil {
		return nil, err
	}
	if result.MinLength, err = copyInt64(schema.MinLength); err != nil {
		return nil, err
	}
	if result.MaxLength, err = copyInt64(schema.MaxLength); err != nil {
		return nil, err
	}
	if result.Minimum, err = copyFloat64(schema.Minimum); err != nil {
		return nil, err
	}
	if result.Maximum, err = copyFloat64(schema.Maximum); err != nil {
		return nil, err
	}
	if schema.Items != nil {
		result.Items, err = convertJSONSchemaToGenai(schema.Items)
		if err != nil {
			return nil, err
		}
	}
	if len(schema.Properties) != 0 {
		result.Properties = make(map[string]*genai.Schema, len(schema.Properties))
		for name, property := range schema.Properties {
			converted, err := convertJSONSchemaToGenai(property)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			result.Properties[name] = converted
		}
	}
	result.Required = append([]string(nil), schema.Required...)
	return result, nil
}

func genaiType(value string) (genai.Type, error) {
	switch value {
	case "string":
		return genai.TypeString, nil
	case "number":
		return genai.TypeNumber, nil
	case "integer":
		return genai.TypeInteger, nil
	case "boolean":
		return genai.TypeBoolean, nil
	case "array":
		return genai.TypeArray, nil
	case "object":
		return genai.TypeObject, nil
	default:
		return "", fmt.Errorf("unsupported schema type %q", value)
	}
}

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

func deploymentDigest(manifest []byte, deployment *Deployment) (string, error) {
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
func (d *Deployment) EntrypointDefinition() (*AgentDefinition, error) {
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
func (d *Deployment) RootName() (string, error) {
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
func (d *Deployment) Agent(name string) (*AgentDefinition, bool) {
	for index := range d.Spec.Agents {
		if d.Spec.Agents[index].Name == name {
			return &d.Spec.Agents[index], true
		}
	}
	return nil, false
}

// DefinitionRef returns the deployment-level identity used by audit and MCP.
func (d *Deployment) DefinitionRef() DefinitionRef {
	return DefinitionRef{
		Name:    d.Metadata.Name,
		Version: d.Metadata.Version,
		Digest:  d.Digest,
	}
}
