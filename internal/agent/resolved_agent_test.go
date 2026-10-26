package agent

import deploymentpkg "github.com/ml8s/liki-agent-runtime/internal/deployment"

import "testing"

func mustRuntimeAgentDefinition(t testing.TB, resolved deploymentpkg.ResolvedAgentDefinition) RuntimeAgentDefinition {
	t.Helper()
	runtime, err := NewRuntimeAgentDefinition(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
