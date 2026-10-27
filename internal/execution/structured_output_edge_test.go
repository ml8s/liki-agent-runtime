package execution

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ml8s/agent-runtime/internal/deployment"
)

func testStructuredDefinition(t *testing.T) *deployment.ResolvedAgentDefinition {
	t.Helper()
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(`{
		"type":"object",
		"additionalProperties":false,
		"required":["answer"],
		"properties":{"answer":{"type":"string"}}
	}`), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &deployment.ResolvedAgentDefinition{
		AgentDefinition: deployment.AgentDefinition{
			Output: deployment.OutputDefinition{
				Schema:      deployment.FileReference{Path: "schema.json"},
				TextPointer: "/answer",
			},
		},
		ResolvedOutput: resolved,
	}
}

func TestJSONStringAtPointerRootString(t *testing.T) {
	got, err := jsonStringAtPointer("root string", "")
	if err != nil {
		t.Fatalf("jsonStringAtPointer() error = %v", err)
	}
	if got != "root string" {
		t.Fatalf("jsonStringAtPointer() = %q, want root string", got)
	}
}

func TestJSONStringAtPointerRootNotString(t *testing.T) {
	if _, err := jsonStringAtPointer(map[string]any{}, ""); err == nil {
		t.Fatal("jsonStringAtPointer() accepted a non-string root")
	}
}

func TestJSONStringAtPointerInvalidPointer(t *testing.T) {
	if _, err := jsonStringAtPointer(map[string]any{}, "no-leading-slash"); err == nil {
		t.Fatal("jsonStringAtPointer() accepted a non-RFC6901 pointer")
	}
}

func TestJSONStringAtPointerEvalError(t *testing.T) {
	if _, err := jsonStringAtPointer(map[string]any{}, "/missing"); err == nil {
		t.Fatal("jsonStringAtPointer() accepted a missing key")
	}
}

func TestJSONStringAtPointerNonStringValue(t *testing.T) {
	if _, err := jsonStringAtPointer(map[string]any{"answer": 42}, "/answer"); err == nil {
		t.Fatal("jsonStringAtPointer() accepted a non-string value")
	}
}

func TestSchemaValueConversions(t *testing.T) {
	integerValue := schemaValue(json.Number("42"))
	if integerValue != int64(42) {
		t.Fatalf("schemaValue(42) = %#v, want int64(42)", integerValue)
	}
	floatValue := schemaValue(json.Number("0.5"))
	if floatValue != float64(0.5) {
		t.Fatalf("schemaValue(0.5) = %#v, want float64(0.5)", floatValue)
	}
	bigValue := schemaValue(json.Number("1e400"))
	if bigValue != "1e400" {
		t.Fatalf("schemaValue(1e400) = %#v, want string", bigValue)
	}
}

func TestSchemaValueNestedContainers(t *testing.T) {
	input := map[string]any{
		"count": json.Number("3"),
		"list":  []any{json.Number("1"), "text"},
	}
	result := schemaValue(input)
	nested, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("schemaValue() type = %T, want map", result)
	}
	if nested["count"] != int64(3) {
		t.Fatalf("nested count = %#v, want int64(3)", nested["count"])
	}
	list, ok := nested["list"].([]any)
	if !ok || list[0] != int64(1) || list[1] != "text" {
		t.Fatalf("nested list = %#v", nested["list"])
	}
}

func TestParseStructuredOutputUnmarshalableValue(t *testing.T) {
	definition := testStructuredDefinition(t)
	_, err := ParseStructuredOutput(map[string]any{
		"answer": make(chan int),
	}, definition)
	if err == nil {
		t.Fatal("ParseStructuredOutput() accepted an unmarshalable value")
	}
}
