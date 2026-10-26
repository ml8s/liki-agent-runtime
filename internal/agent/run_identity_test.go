package agent

import (
	"errors"
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

func TestRunIdentityValidate(t *testing.T) {
	valid := RunIdentity{RunID: "run_1", ThreadID: "thread_1", UserID: "user_1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	testCases := []struct {
		name     string
		identity RunIdentity
		code     string
	}{
		{name: "missing run", identity: RunIdentity{ThreadID: "thread_1", UserID: "user_1"}, code: domain.CodeRunIDRequired},
		{name: "invalid run", identity: RunIdentity{RunID: "bad\nrun", ThreadID: "thread_1", UserID: "user_1"}, code: domain.CodeRunIDInvalid},
		{name: "missing thread", identity: RunIdentity{RunID: "run_1", UserID: "user_1"}, code: domain.CodeThreadIDRequired},
		{name: "missing user", identity: RunIdentity{RunID: "run_1", ThreadID: "thread_1"}, code: domain.CodeUserIDRequired},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.identity.Validate()
			var domainErr *domain.Error
			if err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
			if !errors.As(err, &domainErr) || domainErr.Code != testCase.code {
				t.Fatalf("Validate() error = %v, want %s", err, testCase.code)
			}
		})
	}
}
