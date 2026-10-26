package execution

import (
	"os"
	"path/filepath"
	"testing"

	deployment "github.com/ml8s/liki-agent-runtime/internal/deployment"
)

func loadedTestDeployment(t testing.TB) *deployment.AgentDeployment {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"agent-deployment.json": `{
			"schema_version": "liki.agent-deployment.v1",
			"metadata": {"name": "test-agent", "version": "1.0.0"},
			"spec": {
				"mcpServers": [],
				"agents": [{
					"name": "main",
					"version": "1.0.0",
					"description": "structured test agent",
					"mode": "chat",
					"instruction": {"path": "instruction.md"},
					"output": {
						"schema": {"path": "output.schema.json"},
						"textPointer": "/answer"
					},
					"tools": {"allow": {}}
				}]
			}
		}`,
		"instruction.md": "You are a generic test agent.",
		"output.schema.json": `{
			"type": "object",
			"additionalProperties": false,
			"required": ["answer"],
			"properties": {"answer": {"type": "string"}, "confidence": {"type": "number", "minimum": 0, "maximum": 1}}
		}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deployment, err := deployment.LoadAgentDeployment(filepath.Join(root, "agent-deployment.json"))
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}
