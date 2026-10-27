package agent

import (
	"testing"

	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"
)

func mustRuntimeAgentDefinition(t testing.TB, resolved deploymentpkg.ResolvedAgentDefinition) RuntimeAgentDefinition {
	t.Helper()
	runtime, err := NewRuntimeAgentDefinition(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
