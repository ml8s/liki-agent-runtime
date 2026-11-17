package agent

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	deploymentpkg "github.com/ml8s/agentgo/internal/deployment"
)

func mustRuntimeAgentDefinition(t testing.TB, resolved deploymentpkg.ResolvedAgentDefinition) RuntimeAgentDefinition {
	t.Helper()
	runtime, err := NewRuntimeAgentDefinition(resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestNativeStructuredOutputSkipsRawSchema(t *testing.T) {
	resolved := deploymentpkg.ResolvedAgentDefinition{
		AgentDefinition: deploymentpkg.AgentDefinition{
			Output: deploymentpkg.OutputDefinition{
				Schema:      deploymentpkg.FileReference{Path: "output.schema.json"},
				TextPointer: "/answer",
			},
		},
		OutputSchema:    &jsonschema.Schema{Type: "object"},
		RawOutputSchema: map[string]any{"type": "object"},
	}
	native, err := NewRuntimeAgentDefinition(resolved, true)
	if err != nil {
		t.Fatal(err)
	}
	if native.RawOutputSchema != nil {
		t.Fatalf("native structured output: RawOutputSchema = %v, want nil", native.RawOutputSchema)
	}
	shimmed, err := NewRuntimeAgentDefinition(resolved, false)
	if err != nil {
		t.Fatal(err)
	}
	if shimmed.RawOutputSchema == nil {
		t.Fatal("shimmed structured output: RawOutputSchema is nil, want the raw schema")
	}
}
