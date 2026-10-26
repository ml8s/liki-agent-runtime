package pipeline_test

import (
	"os"
	"path/filepath"
	"testing"

	agentruntime "github.com/ml8s/liki-agent-runtime/internal/agent"
)

// FourAgentArtifactIsLoadable proves the generic runtime can describe a
// standard ADK delegation chain without embedding any domain workflow.
func TestFourAgentArtifactIsLoadable(t *testing.T) {
	root := t.TempDir()
	manifest := `{
		"schema_version": "liki.agent-deployment.v1", "metadata": {"name": "generic-four-agents", "version": "1.0.0"},
		"spec": {
			"mcpServers": [{"name": "test", "endpointEnv": "TEST_MCP_ENDPOINT"}],
			"agents": [
				{
					"name": "main", "version": "1.0.0",
					"description": "generic entrypoint",
					"mode": "chat",
					"sub_agents": [{"name": "planner"}],
					"instruction": {"path": "instruction.md"},
					"skills": {"root": "/skills"},
					"tools": {"allow": {"skilltoolset": ["list_skills", "load_skill", "load_skill_resource"]}}
				},
				{
					"name": "planner", "version": "1.0.0",
					"description": "generic planner",
					"mode": "chat",
					"sub_agents": [{"name": "worker"}],
					"instruction": {"path": "instruction.md"},
					"skills": {"root": "/skills"},
					"tools": {"allow": {"skilltoolset": ["list_skills", "load_skill", "load_skill_resource"]}}
				},
				{
					"name": "worker", "version": "1.0.0",
					"description": "generic worker",
					"mode": "task",
					"sub_agents": [{"name": "reviewer"}],
					"instruction": {"path": "instruction.md"},
					"skills": {"root": "/skills"}, "tools": {"allow": {"test": ["engine_tool"], "skilltoolset": ["list_skills", "load_skill", "load_skill_resource"]}}
				},
				{
					"name": "reviewer", "version": "1.0.0",
					"description": "generic reviewer",
					"mode": "single_turn",
					"sub_agents": [],
					"instruction": {"path": "instruction.md"},
					"skills": {"root": "/skills"},
					"tools": {"allow": {"skilltoolset": ["list_skills", "load_skill", "load_skill_resource"]}}
				}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(root, "instruction.md"), []byte("generic instruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent-deployment.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	deployment, err := agentruntime.LoadAgentDeployment(filepath.Join(root, "agent-deployment.json"))
	if err != nil {
		t.Fatalf("LoadAgentDeployment() error = %v", err)
	}
	rootName, err := deployment.RootName()
	if err != nil {
		t.Fatal(err)
	}
	if rootName != "main" || len(deployment.Spec.Agents) != 4 {
		t.Fatalf("root=%q agents=%d, want main/4", rootName, len(deployment.Spec.Agents))
	}

	byName := map[string]agentruntime.ResolvedAgentDefinition{}
	for _, definition := range deployment.Spec.Agents {
		byName[definition.Name] = definition
	}
	wantEdges := map[string][]string{
		"main":     {"planner"},
		"planner":  {"worker"},
		"worker":   {"reviewer"},
		"reviewer": {},
	}
	for parent, children := range wantEdges {
		if len(byName[parent].SubAgents) != len(children) {
			t.Fatalf("%s sub-agents = %#v, want %v", parent, byName[parent].SubAgents, children)
		}
		for index, child := range children {
			if byName[parent].SubAgents[index].Name != child {
				t.Fatalf("%s sub-agent %d = %q, want %q", parent, index, byName[parent].SubAgents[index].Name, child)
			}
		}
	}
}
