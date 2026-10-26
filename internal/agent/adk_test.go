package agent

import deploymentpkg "github.com/ml8s/liki-agent-runtime/internal/deployment"

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

type staticToolset struct{}

func (staticToolset) Name() string { return "static" }

func (staticToolset) Tools(adkagent.ReadonlyContext) ([]tool.Tool, error) {
	return nil, nil
}

func TestAgentDefinitionCompilesToStandardADKConfig(t *testing.T) {
	temperature := float32(0.25)
	outputSchema := &jsonschema.Schema{Type: "object"}
	resolved, err := outputSchema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	declaration := deploymentpkg.AgentDefinition{
		Name:        "worker",
		Description: "generic worker",
		Mode:        deploymentpkg.AgentModeTask,
		Output: deploymentpkg.OutputDefinition{
			Schema:      deploymentpkg.FileReference{Path: "output.schema.json"},
			TextPointer: "/answer",
		},
		Tools: deploymentpkg.ToolAllowlist{Allow: map[string][]string{"test": {"test_tool"}}},
	}
	definition, err := NewRuntimeAgentDefinition(deploymentpkg.ResolvedAgentDefinition{
		AgentDefinition: declaration,
		InstructionText: "generic instruction",
		OutputSchema:    outputSchema,
		ResolvedOutput:  resolved,
		RawOutputSchema: map[string]any{"type": "object", "additionalProperties": false},
	})
	if err != nil {
		t.Fatal(err)
	}

	beforeModel := func(adkagent.Context, *model.LLMRequest) (*model.LLMResponse, error) { return nil, nil }
	config := definition.ADKConfig(ADKAgentRuntime{
		Toolsets:             []tool.Toolset{staticToolset{}},
		SubAgents:            []adkagent.Agent{nil},
		Temperature:          temperature,
		OutputKey:            "structured_output",
		IsEntrypoint:         true,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{beforeModel},
	})

	if config.Name != "worker" || config.Description != "generic worker" {
		t.Fatalf("identity = %q/%q", config.Name, config.Description)
	}
	if config.Mode != llmagent.ModeTask || config.Instruction != "generic instruction" {
		t.Fatalf("mode/instruction = %v/%q", config.Mode, config.Instruction)
	}
	if config.InstructionProvider == nil {
		t.Fatal("instruction provider is required so literal JSON braces are not state placeholders")
	}
	if config.Model != nil || len(config.SubAgents) != 1 {
		t.Fatalf("runtime dependencies = %#v", config)
	}
	if len(config.Toolsets) != 1 || config.Toolsets[0].Name() != "static" {
		t.Fatalf("toolsets = %#v", config.Toolsets)
	}
	if config.GenerateContentConfig == nil || config.GenerateContentConfig.Temperature == nil ||
		*config.GenerateContentConfig.Temperature != temperature {
		t.Fatalf("generation config = %#v", config.GenerateContentConfig)
	}
	if config.OutputSchema != definition.GenaiOutputSchema || config.OutputKey != "structured_output" {
		t.Fatalf("structured output = %#v/%q", config.OutputSchema, config.OutputKey)
	}
	if !config.DisallowTransferToParent || !config.DisallowTransferToPeers {
		t.Fatalf("transfer policy = parent:%v peers:%v", config.DisallowTransferToParent, config.DisallowTransferToPeers)
	}
	if len(config.BeforeModelCallbacks) != 2 {
		t.Fatalf("callbacks = %#v", config.BeforeModelCallbacks)
	}
}

func TestADKConfigCarriesMaxOutputTokens(t *testing.T) {
	t.Parallel()
	definition := RuntimeAgentDefinition{Definition: deploymentpkg.ResolvedAgentDefinition{AgentDefinition: deploymentpkg.AgentDefinition{Name: "worker"}}}
	bounded := definition.ADKConfig(ADKAgentRuntime{
		Temperature:     0.2,
		MaxOutputTokens: 2048,
	})
	if bounded.GenerateContentConfig == nil || bounded.GenerateContentConfig.MaxOutputTokens != 2048 {
		t.Fatalf("generation config = %#v, want MaxOutputTokens 2048", bounded.GenerateContentConfig)
	}
	unbounded := definition.ADKConfig(ADKAgentRuntime{Temperature: 0.2})
	if unbounded.GenerateContentConfig == nil || unbounded.GenerateContentConfig.MaxOutputTokens != 0 {
		t.Fatalf("generation config = %#v, want MaxOutputTokens 0 (unset)", unbounded.GenerateContentConfig)
	}
}

func TestRawJSONSchemaIsUsedOnStandardOpenAICompatibleWire(t *testing.T) {
	raw := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"answer": map[string]any{"type": "string"}},
	}
	callback := rawOutputSchemaCallback(raw)
	request := &model.LLMRequest{Config: &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
		ResponseSchema:   &genai.Schema{Type: genai.TypeObject},
	}}

	if _, err := callback(nil, request); err != nil {
		t.Fatal(err)
	}
	if request.Config.ResponseSchema != nil {
		t.Fatal("lossy genai schema was sent to provider")
	}
	if request.Config.ResponseJsonSchema == nil {
		t.Fatal("raw JSON Schema was not sent to provider")
	}
	sent, err := json.Marshal(request.Config.ResponseJsonSchema)
	if err != nil {
		t.Fatal(err)
	}
	encodedRaw, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var sentValue, wantValue any
	if err := json.Unmarshal(sent, &sentValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedRaw, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sentValue, wantValue) {
		t.Fatalf("raw schema altered on the wire: got %#v, want %#v", sentValue, wantValue)
	}
}
