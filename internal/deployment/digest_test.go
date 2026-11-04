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

func TestDeploymentDigestCoversBehaviorSettings(t *testing.T) {
	base := writeDeployment(t, t.TempDir(), validManifest())
	root := t.TempDir()

	modelManifest := strings.Replace(
		validManifest(),
		`"instruction": {"path": "instruction.md"}`,
		`"instruction": {"path": "instruction.md"}, "model": "model-b"`,
		1,
	)
	modelChanged := writeDeployment(t, root, modelManifest)
	if base.Digest == modelChanged.Digest {
		t.Fatal("deployment digest did not cover agent model")
	}

	temperatureManifest := strings.Replace(
		modelManifest,
		`"model": "model-b"`,
		`"model": "model-b", "temperature": 0.25`,
		1,
	)
	temperatureChanged := writeDeployment(t, t.TempDir(), temperatureManifest)
	if modelChanged.Digest == temperatureChanged.Digest {
		t.Fatal("deployment digest did not cover temperature")
	}

	tokensManifest := strings.Replace(
		temperatureManifest,
		`"temperature": 0.25`,
		`"temperature": 0.25, "maxOutputTokens": 128`,
		1,
	)
	tokensChanged := writeDeployment(t, t.TempDir(), tokensManifest)
	if temperatureChanged.Digest == tokensChanged.Digest {
		t.Fatal("deployment digest did not cover maxOutputTokens")
	}

	skillsManifest := strings.Replace(
		tokensManifest,
		`"skills": {"root": "/skills"}`,
		`"skills": {"root": "/other-skills", "preload": "complete"}`,
		1,
	)
	skillsChanged := writeDeployment(t, t.TempDir(), skillsManifest)
	if tokensChanged.Digest == skillsChanged.Digest {
		t.Fatal("deployment digest did not cover skills binding")
	}
}

func TestLoadedDeploymentClonePreservesSemanticIdentity(t *testing.T) {
	base := writeDeployment(t, t.TempDir(), validManifest())
	clone, err := base.Clone()
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if clone.Digest != base.Digest || clone.ManifestDigest != base.ManifestDigest {
		t.Fatalf("clone identity = %q/%q, want %q/%q", clone.Digest, clone.ManifestDigest, base.Digest, base.ManifestDigest)
	}
	if err := clone.ValidateIntegrity(); err != nil {
		t.Fatalf("ValidateIntegrity() error = %v", err)
	}
}
