// Package testagent provides a generic AgentDeployment for adapter tests.
// Domain behavior intentionally remains outside agent-runtime.
package testagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"
)

// ResolvedAgents is a test-fixture conversion helper. Production deployments
// must come from deployment.LoadAgentDeployment, which resolves external files
// and computes identity.
func ResolvedAgents(definitions []deploymentpkg.AgentDefinition) []deploymentpkg.ResolvedAgentDefinition {
	resolved := make([]deploymentpkg.ResolvedAgentDefinition, 0, len(definitions))
	for index := range definitions {
		definition := &definitions[index]
		resolved = append(resolved, deploymentpkg.ResolvedAgentDefinition{AgentDefinition: *definition})
	}
	return resolved
}

// AgentDeployment loads a generic structured AgentDeployment for adapter tests.
func AgentDeployment(t testing.TB) *deploymentpkg.AgentDeployment {
	t.Helper()
	t.Setenv("TEST_MCP_ENDPOINT", "in-memory://test")
	root := t.TempDir()
	files := map[string]string{
		"agent-deployment.json": `{
			"schema_version": "agent.deployment.v1", "metadata": {"name": "test-agent", "version": "1.0.0"},
			"spec": {
				"mcpServers": [{"name": "test", "endpointEnv": "TEST_MCP_ENDPOINT"}],
				"agents": [{
				"name": "main",
				"version": "1.0.0",
				"description": "generic test agent",
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
				"answer": {"type": "string", "minLength": 1},
				"confidence": {"type": "number", "minimum": 0, "maximum": 1}
			}
		}`,
	}
	skillRoot := filepath.Join(root, "skills-root")
	WriteMinimalSkill(t, skillRoot)
	files["agent-deployment.json"] = strings.ReplaceAll(
		files["agent-deployment.json"], "SKILLS_ROOT_PLACEHOLDER", skillRoot)
	return loadTestDeployment(t, root, files)
}

// PlainAgentDeployment loads the generic fixture without an output schema.
// The runtime treats loaded aggregates as immutable; tests that need a plain
// Agent must select this fixture rather than mutate a loaded snapshot.
func PlainAgentDeployment(t testing.TB) *deploymentpkg.AgentDeployment {
	t.Helper()
	t.Setenv("TEST_MCP_ENDPOINT", "in-memory://test")
	root := t.TempDir()
	files := map[string]string{
		"agent-deployment.json": `{
			"schema_version": "agent.deployment.v1", "metadata": {"name": "test-agent", "version": "1.0.0"},
			"spec": {
				"mcpServers": [{"name": "test", "endpointEnv": "TEST_MCP_ENDPOINT"}],
				"agents": [{
				"name": "main",
				"version": "1.0.0",
				"description": "generic plain test agent",
					"mode": "chat",
					"sub_agents": [],
				"instruction": {"path": "instruction.md"},
				"skills": {"root": "SKILLS_ROOT_PLACEHOLDER"},
				"tools": {"allow": {"test": ["test_tool"], "skilltoolset": ["list_skills", "load_skill", "load_skill_resource"]}}
								}]
			}
		}`,
		"instruction.md": "You are a generic adapter test agent.",
	}
	skillRoot := filepath.Join(root, "skills-root")
	WriteMinimalSkill(t, skillRoot)
	files["agent-deployment.json"] = strings.ReplaceAll(
		files["agent-deployment.json"], "SKILLS_ROOT_PLACEHOLDER", skillRoot)
	return loadTestDeployment(t, root, files)
}

func loadTestDeployment(
	t testing.TB,
	root string,
	files map[string]string,
) *deploymentpkg.AgentDeployment {
	t.Helper()
	skillRoot := filepath.Join(root, "skills-root")
	WriteMinimalSkill(t, skillRoot)
	files["agent-deployment.json"] = strings.ReplaceAll(
		files["agent-deployment.json"], "SKILLS_ROOT_PLACEHOLDER", skillRoot)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	artifact, err := deploymentpkg.LoadAgentDeployment(filepath.Join(root, "agent-deployment.json"))
	if err != nil {
		t.Fatalf("LoadAgentDeployment() error = %v", err)
	}
	return artifact
}
