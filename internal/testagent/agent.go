// Package testagent provides a generic AgentDeployment for adapter tests.
// Domain behavior intentionally remains outside liki-agents.
package testagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ml8s/liki-agents/internal/agent"
)

// Deployment loads a generic structured AgentDeployment for adapter tests.
func Deployment(t *testing.T) *agent.Deployment {
	t.Helper()
	t.Setenv("TEST_MCP_ENDPOINT", "in-memory://test")
	root := t.TempDir()
	files := map[string]string{
		"agent-deployment.json": `{
			"metadata": {"name": "adapter-agent", "version": "1.0.0"},
			"spec": {
				"mcpServers": [{"name": "test", "endpointEnv": "TEST_MCP_ENDPOINT"}],
				"agents": [{
					"name": "main",
					"version": "1.0.0",
					"description": "generic adapter test agent",
						"mode": "chat",
						"sub_agents": [],
					"instruction": {"path": "instruction.md"},
					"skills": {"root": "SKILLS_ROOT_PLACEHOLDER"},
					"output": {
						"schema": {"path": "output.schema.json"},
						"textPointer": "/answer"
					},
					"tools": {"allow": {"test": ["test_tool"], "skilltoolset": ["list_skills", "load_skill", "load_skill_resource"]}}
									}]
			}
		}`,
		"instruction.md": "You are a generic adapter test agent.",
		"output.schema.json": `{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type": "object",
			"additionalProperties": false,
			"required": ["answer"],
			"properties": {
				"answer": {"type": "string", "minLength": 1}
			}
		}`,
	}
	skillRoot := filepath.Join(root, "skills-root")
	writeMinimalSkill(t, skillRoot)
	files["agent-deployment.json"] = strings.ReplaceAll(
		files["agent-deployment.json"], "SKILLS_ROOT_PLACEHOLDER", skillRoot)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deployment, err := agent.LoadAgentDeployment(filepath.Join(root, "agent-deployment.json"))
	if err != nil {
		t.Fatalf("LoadAgentDeployment() error = %v", err)
	}
	return deployment
}
