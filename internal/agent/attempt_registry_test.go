package agent

import (
	"testing"

	"github.com/ml8s/agent-runtime/internal/domain"
	"github.com/ml8s/agent-runtime/internal/execution"
)

func testAttemptScope(runID string) *runExecution {
	return &runExecution{
		identity: execution.RunIdentity{
			RunID:    domain.ID(runID),
			ThreadID: domain.ID("thread_" + runID),
			UserID:   "user_test",
		},
		activeCalls: make(map[string]llmCallRuntime),
	}
}

func TestAttemptRegistryRejectsDuplicateAttempts(t *testing.T) {
	registry := newAttemptRegistry()
	scope := testAttemptScope("run_1")
	if !registry.begin("attempt_1", "context_1", scope) {
		t.Fatal("first attempt registration was rejected")
	}
	if registry.begin("attempt_1", "context_1", testAttemptScope("run_2")) {
		t.Fatal("duplicate attempt ID was accepted")
	}
	if registry.begin("", "context_1", testAttemptScope("run_3")) ||
		registry.begin("attempt_3", "context_1", nil) {
		t.Fatal("empty identity was accepted")
	}
}

func TestAttemptRegistryLookupOrder(t *testing.T) {
	registry := newAttemptRegistry()
	scope := testAttemptScope("run_1")
	registry.begin("attempt_1", "context_1", scope)

	if found, ok := registry.lookup("attempt_1"); !ok || found != scope {
		t.Fatal("attempt lookup failed")
	}
	if found, ok := registry.lookup("context_1"); !ok || found != scope {
		t.Fatal("single-attempt context lookup failed")
	}

	other := testAttemptScope("run_2")
	registry.begin("attempt_2", "context_1", other)
	if _, ok := registry.lookup("context_1"); ok {
		t.Fatal("ambiguous context lookup guessed a scope")
	}
	if found, ok := registry.lookup("attempt_2"); !ok || found != other {
		t.Fatal("second attempt lookup failed")
	}
}

func TestAttemptRegistryInvocationBridgeOutlivesInvocationNotAttempt(t *testing.T) {
	registry := newAttemptRegistry()
	scope := testAttemptScope("run_1")
	registry.begin("attempt_1", "context_1", scope)
	registry.bindInvocation("session_1", "invocation_1", "attempt_1")

	if attemptID, ok := registry.attemptForInvocation("session_1", "invocation_1"); !ok || attemptID != "attempt_1" {
		t.Fatalf("invocation bridge = %q ok=%v, want attempt_1", attemptID, ok)
	}

	// An ADK invocation ends: only its bridge disappears. The attempt must
	// stay registered for later invocations in the same protocol context.
	registry.unbindInvocations("attempt_1")
	if _, ok := registry.attemptForInvocation("session_1", "invocation_1"); ok {
		t.Fatal("invocation bridge survived its invocation")
	}
	if found, ok := registry.lookup("attempt_1"); !ok || found != scope {
		t.Fatal("attempt was released with an invocation bridge")
	}

	// The next invocation rebinds and resolves again.
	registry.bindInvocation("session_1", "invocation_2", "attempt_1")
	if _, ok := registry.attemptForInvocation("session_1", "invocation_2"); !ok {
		t.Fatal("rebinding a later invocation failed")
	}
}

func TestAttemptRegistryUnbindAttemptAndForgetContext(t *testing.T) {
	registry := newAttemptRegistry()
	first := testAttemptScope("run_1")
	second := testAttemptScope("run_2")
	registry.begin("attempt_1", "context_1", first)
	registry.begin("attempt_2", "context_1", second)
	registry.bindInvocation("session_1", "invocation_1", "attempt_1")

	registry.unbindAttempt("attempt_1")
	for _, key := range []string{"attempt_1", "session_1", "invocation_1"} {
		if _, ok := registry.lookup(key); key != "invocation_1" && ok {
			t.Fatalf("key %q survived unbindAttempt", key)
		}
	}
	if _, ok := registry.attemptForInvocation("session_1", "invocation_1"); ok {
		t.Fatal("invocation bridge survived its attempt")
	}
	if found, ok := registry.lookup("attempt_2"); !ok || found != second {
		t.Fatal("sibling attempt was released by unbindAttempt")
	}

	registry.forgetContext("context_1")
	if _, ok := registry.lookup("attempt_2"); ok {
		t.Fatal("context-owned attempt survived forgetContext")
	}
}
