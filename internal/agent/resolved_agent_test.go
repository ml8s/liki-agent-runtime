package agent

import "testing"

func mustRuntimeAgentDefinition(t testing.TB, resolved ResolvedAgentDefinition) RuntimeAgentDefinition {
	t.Helper()
	runtime, err := NewRuntimeAgentDefinition(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
