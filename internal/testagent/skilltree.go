package testagent

import (
	"os"
	"path/filepath"
	"testing"
)

// WriteMinimalSkill creates a valid ADK skill rootFS for runtime preload.
// WriteMinimalSkill creates a valid ADK skill rootFS for runtime preload.
func WriteMinimalSkill(t testing.TB, root string) {
	t.Helper()
	dir := filepath.Join(root, "testskill")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := `---
name: testskill
description: Minimal skill used by runtime tests to exercise the skilltoolset binding. Provides one reference card for load_skill_resource coverage.
---

# testskill

Load references/card.md for details.`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "card.md"), []byte("# card\n\nBody."), 0o600); err != nil {
		t.Fatal(err)
	}
}
