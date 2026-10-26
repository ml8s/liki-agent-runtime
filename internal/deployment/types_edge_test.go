package deployment

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func testDeploymentForEdge() *AgentDeployment {
	return &AgentDeployment{
		SchemaVersion: ContractVersion,
		Metadata:      DeploymentMetadata{Name: "test", Version: "1.0.0"},
		Spec: DeploymentSpec{
			MCPServers: []MCPServerDefinition{
				{Name: "engine", EndpointEnv: "ENGINE_URL"},
			},
			Agents: ResolvedAgentDefinitions([]AgentDefinition{
				{
					Name:    "root",
					Version: "1.0.0",
					Mode:    AgentModeChat,
					SubAgents: []AgentReference{
						{Name: "child"},
					},
					Tools: ToolAllowlist{Allow: map[string][]string{
						"engine": {"tool_a"},
					}},
				},
				{
					Name:    "child",
					Version: "1.0.0",
					Mode:    AgentModeChat,
					Tools: ToolAllowlist{Allow: map[string][]string{
						"engine": {"tool_b"},
					}},
				},
			}),
		},
	}
}

func TestAgentLookupByName(t *testing.T) {
	deployment := testDeploymentForEdge()
	agent, found := deployment.Agent("child")
	if !found || agent == nil || agent.Name != "child" {
		t.Fatalf("Agent(%q) = %#v/%v, want child", "child", agent, found)
	}
	if _, found := deployment.Agent("missing"); found {
		t.Fatal("Agent(missing) found, want false")
	}
}

func TestDefinitionRef(t *testing.T) {
	deployment := testDeploymentForEdge()
	deployment.Digest = "sha256:abc"
	ref := deployment.DefinitionRef()
	if ref.Name != "test" || ref.Version != "1.0.0" || ref.Digest != "sha256:abc" {
		t.Fatalf("DefinitionRef() = %#v", ref)
	}
}

func TestSkillBinding(t *testing.T) {
	if SkillBinding(&AgentDefinition{}) != nil {
		t.Fatal("SkillBinding(nil skills) != nil")
	}
	binding := &SkillsBinding{Root: "/skills", Preload: "frontmatter"}
	got := SkillBinding(&AgentDefinition{Skills: binding})
	if got == nil || got.Root != "/skills" {
		t.Fatalf("SkillBinding() = %#v", got)
	}
	// The returned binding is a copy: mutating it must not affect the source.
	got.Root = "/mutated"
	if binding.Root != "/skills" {
		t.Fatal("SkillBinding() returned a non-defensive copy")
	}
}

func TestExposesModelText(t *testing.T) {
	if ExposesModelText(nil) {
		t.Fatal("ExposesModelText(nil) = true, want false")
	}
	plain := &AgentDefinition{Mode: AgentModeChat}
	if !ExposesModelText(plain) {
		t.Fatal("ExposesModelText(plain) = false, want true")
	}
	structured := &AgentDefinition{
		Output: OutputDefinition{
			Schema:      FileReference{Path: "schema.json"},
			TextPointer: "/answer",
		},
	}
	if ExposesModelText(structured) {
		t.Fatal("ExposesModelText(structured) = true, want false")
	}
}

func TestToolReferenceFor(t *testing.T) {
	definition := &AgentDefinition{
		Tools: ToolAllowlist{Allow: map[string][]string{
			"zeta":   {"tool_z"},
			"alpha":  {"tool_a", "shared_tool"},
			"middle": {"tool_m"},
		}},
	}
	ref, found := definition.ToolReferenceFor("tool_a")
	if !found || ref.Server != "alpha" || ref.Name != "tool_a" {
		t.Fatalf("ToolReferenceFor(tool_a) = %#v/%v", ref, found)
	}
	// Server order is deterministic (sorted), not map-iteration order.
	ref, found = definition.ToolReferenceFor("shared_tool")
	if !found || ref.Server != "alpha" {
		t.Fatalf("ToolReferenceFor(shared_tool) = %#v/%v", ref, found)
	}
	if _, found := definition.ToolReferenceFor("missing"); found {
		t.Fatal("ToolReferenceFor(missing) found, want false")
	}
}

func TestValidateAgentRechecksInvariants(t *testing.T) {
	valid := &AgentDefinition{
		Name:        "main",
		Description: "test agent",
		Version:     "1.0.0",
		Mode:        AgentModeChat,
		Instruction: FileReference{Path: "instruction.md"},
		Tools:       ToolAllowlist{Allow: map[string][]string{}},
	}
	if err := valid.ValidateAgent(map[string]struct{}{}); err != nil {
		t.Fatalf("ValidateAgent() error = %v", err)
	}

	invalid := &AgentDefinition{
		Name:        "main",
		Description: "test agent",
		Version:     "1.0.0",
		Mode:        AgentModeChat,
		Instruction: FileReference{Path: "instruction.md"},
		Tools: ToolAllowlist{Allow: map[string][]string{
			"unknown": {"tool"},
		}},
	}
	if err := invalid.ValidateAgent(map[string]struct{}{}); err == nil {
		t.Fatal("ValidateAgent() accepted an unknown MCP server")
	}
}

func TestMCPServerDefinitionValidation(t *testing.T) {
	valid := &MCPServerDefinition{Name: "engine", EndpointEnv: "ENGINE_URL"}
	if err := valid.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}

	testCases := []struct {
		name     string
		endpoint string
	}{
		{name: "", endpoint: "URL"},
		{name: " padded ", endpoint: "URL"},
		{name: "engine", endpoint: ""},
		{name: "engine", endpoint: " padded "},
	}
	for _, testCase := range testCases {
		server := &MCPServerDefinition{Name: testCase.name, EndpointEnv: testCase.endpoint}
		if err := server.validate(); err == nil {
			t.Fatalf("validate(%q,%q) accepted invalid server", testCase.name, testCase.endpoint)
		}
	}

	tokenCases := []struct {
		token string
		valid bool
	}{
		{token: "", valid: true},
		{token: "TOKEN", valid: true},
		{token: " padded ", valid: false},
	}
	for _, testCase := range tokenCases {
		server := &MCPServerDefinition{Name: "engine", EndpointEnv: "URL", TokenEnv: testCase.token}
		err := server.validate()
		if testCase.valid && err != nil {
			t.Fatalf("validate(TokenEnv=%q) error = %v", testCase.token, err)
		}
		if !testCase.valid && err == nil {
			t.Fatalf("validate(TokenEnv=%q) accepted padding", testCase.token)
		}
	}
}

func TestValidateOutputPointerEdgeCases(t *testing.T) {
	stringSchema := &jsonschema.Schema{Type: "string"}
	objectSchema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"answer": stringSchema,
		},
	}

	testCases := []struct {
		name    string
		schema  *jsonschema.Schema
		pointer string
		wantErr bool
	}{
		{name: "valid object property", schema: objectSchema, pointer: "/answer"},
		{name: "missing property", schema: objectSchema, pointer: "/missing", wantErr: true},
		{name: "nil schema at root", schema: nil, pointer: "/answer", wantErr: true},
		{name: "traverses string", schema: stringSchema, pointer: "/nested", wantErr: true},
		{name: "invalid pointer", schema: objectSchema, pointer: "no-slash", wantErr: true},
		{
			name:    "array index outside prefix",
			schema:  &jsonschema.Schema{Type: "array", ItemsArray: []*jsonschema.Schema{stringSchema}},
			pointer: "/1",
			wantErr: true,
		},
		{
			name:    "array non-index token",
			schema:  &jsonschema.Schema{Type: "array", Items: stringSchema},
			pointer: "/notanumber",
			wantErr: true,
		},
		{
			name:    "array '-' token with nil items",
			schema:  &jsonschema.Schema{Type: "array"},
			pointer: "/-",
			wantErr: true,
		},
		{
			name:    "array with items nil resolves to nil",
			schema:  &jsonschema.Schema{Type: "array"},
			pointer: "/0",
			wantErr: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateOutputPointer(testCase.schema, testCase.pointer)
			if testCase.wantErr && err == nil {
				t.Fatalf("validateOutputPointer(%q) accepted invalid pointer", testCase.pointer)
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("validateOutputPointer(%q) error = %v", testCase.pointer, err)
			}
		})
	}
}

func TestSelectsString(t *testing.T) {
	if selectsString(nil) {
		t.Fatal("selectsString(nil) = true, want false")
	}
	if !selectsString(&jsonschema.Schema{Type: "string"}) {
		t.Fatal("selectsString(string) = false, want true")
	}
	if selectsString(&jsonschema.Schema{Type: "object"}) {
		t.Fatal("selectsString(object) = true, want false")
	}
	if !selectsString(&jsonschema.Schema{Enum: []any{"a", "b"}}) {
		t.Fatal("selectsString(string enum) = false, want true")
	}
	if selectsString(&jsonschema.Schema{Enum: []any{"a", 42}}) {
		t.Fatal("selectsString(mixed enum) = true, want false")
	}
	if selectsString(&jsonschema.Schema{}) {
		t.Fatal("selectsString(empty) = true, want false")
	}
}

func TestIsDecimal(t *testing.T) {
	validCases := []string{"0", "42", "123456789"}
	for _, value := range validCases {
		if !isDecimal(value) {
			t.Fatalf("isDecimal(%q) = false, want true", value)
		}
	}
	invalidCases := []string{"", "-1", "1.5", "abc", "1a", "+1"}
	for _, value := range invalidCases {
		if isDecimal(value) {
			t.Fatalf("isDecimal(%q) = true, want false", value)
		}
	}
}

func TestEntrypointDefinitionError(t *testing.T) {
	deployment := testDeploymentForEdge()
	if _, err := deployment.EntrypointDefinition(); err != nil {
		t.Fatalf("EntrypointDefinition() error = %v", err)
	}

	// A deployment with zero agents cannot have a root.
	empty := &AgentDeployment{
		SchemaVersion: ContractVersion,
		Metadata:      DeploymentMetadata{Name: "empty", Version: "1.0.0"},
	}
	if _, err := empty.RootName(); err == nil {
		t.Fatal("RootName() on empty deployment accepted zero roots")
	}
}

func TestAgentDigestDeterministic(t *testing.T) {
	deployment := testDeploymentForEdge()
	first, err := agentDigest(deployment.Spec.Agents[0])
	if err != nil {
		t.Fatalf("agentDigest() error = %v", err)
	}
	second, err := agentDigest(deployment.Spec.Agents[0])
	if err != nil {
		t.Fatalf("agentDigest() error = %v", err)
	}
	if first != second {
		t.Fatalf("agentDigest() not deterministic: %s vs %s", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("agentDigest() = %q, want sha256: prefix", first)
	}
}
