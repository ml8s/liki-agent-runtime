package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

const currentContractManifest = `{
	"schema_version":"liki.agent-deployment.v1",
	"metadata":{"name":"generic-agents","version":"1.0.0"},
	"spec":{
		"mcpServers":[{"name":"test","endpointEnv":"TEST_MCP_ENDPOINT"}],
		"agents":[{
			"name":"main",
			"version":"1.0.0",
			"description":"contract test",
			"mode":"chat",
			"sub_agents":[],
			"instruction":{"path":"instruction.md"},
			"tools":{"allow":{"test":["test_tool"]}}
		}]
	}
}`

func TestDeploymentRequiresCurrentContractVersion(t *testing.T) {
	if _, err := decodeAgentDeployment([]byte(currentContractManifest)); err != nil {
		t.Fatalf("current contract rejected: %v", err)
	}

	var missing map[string]any
	if err := json.Unmarshal([]byte(currentContractManifest), &missing); err != nil {
		t.Fatal(err)
	}
	delete(missing, "schema_version")
	raw, _ := json.Marshal(missing)
	if _, err := decodeAgentDeployment(raw); err == nil || !strings.Contains(err.Error(), "unsupported schema_version") {
		t.Fatalf("missing schema_version error = %v", err)
	}
}
