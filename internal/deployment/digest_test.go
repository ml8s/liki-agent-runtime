package deployment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentDigestCoversExternalArtifactsAndGraphSemantics(t *testing.T) {
	root := t.TempDir()
	base := writeDeployment(t, root, validManifest())

	if err := os.WriteFile(filepath.Join(root, "instruction.md"), []byte("changed generic instruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	instructionChanged, err := LoadAgentDeployment(filepath.Join(root, "agent-deployment.json"))
	if err != nil {
		t.Fatal(err)
	}
	if base.Digest == instructionChanged.Digest {
		t.Fatal("deployment digest did not cover instruction artifact")
	}

	if err := os.WriteFile(filepath.Join(root, "instruction.md"), []byte("generic instruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "output.schema.json"), []byte(`{
		"type": "object",
		"additionalProperties": false,
		"required": ["answer", "extra"],
		"properties": {"answer": {"type": "string"}, "extra": {"type": "string"}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	schemaChanged, err := LoadAgentDeployment(filepath.Join(root, "agent-deployment.json"))
	if err != nil {
		t.Fatal(err)
	}
	if base.Digest == schemaChanged.Digest {
		t.Fatal("deployment digest did not cover output schema artifact")
	}

	modeChangedManifest := strings.Replace(validManifest(), `"mode": "chat"`, `"mode": "task"`, 1)
	modeChanged := writeDeployment(t, t.TempDir(), modeChangedManifest)
	if base.Digest == modeChanged.Digest {
		t.Fatal("deployment digest did not cover agent mode")
	}
}
