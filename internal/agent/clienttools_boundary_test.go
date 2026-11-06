package agent

import "testing"

func TestClientToolsAreEntrypointOnly(t *testing.T) {
	if !clientToolsAllowedFor("entrypoint", "entrypoint") {
		t.Fatal("entrypoint agent must receive client tools")
	}
	if clientToolsAllowedFor("domain-worker", "entrypoint") {
		t.Fatal("delegated agent must not receive client tools")
	}
}
