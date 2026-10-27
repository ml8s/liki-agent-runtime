package execution

import (
	"errors"
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

func TestRunIdentityValidate(t *testing.T) {
	valid := RunIdentity{RunID: "run-1", ThreadID: "thread-1", UserID: "user-1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}

	testCases := []struct {
		name string
		want string
	}{
		{name: "run_id_required", want: domain.CodeRunIDRequired},
		{name: "run_id_invalid", want: domain.CodeRunIDInvalid},
		{name: "thread_id_required", want: domain.CodeThreadIDRequired},
		{name: "thread_id_invalid", want: domain.CodeThreadIDInvalid},
		{name: "user_id_required", want: domain.CodeUserIDRequired},
		{name: "user_id_invalid", want: domain.CodeUserIDInvalid},
	}
	build := func(want string) RunIdentity {
		identity := RunIdentity{RunID: "r", ThreadID: "t", UserID: "u"}
		switch want {
		case domain.CodeRunIDRequired:
			identity.RunID = ""
		case domain.CodeRunIDInvalid:
			identity.RunID = domain.ID("\x00")
		case domain.CodeThreadIDRequired:
			identity.ThreadID = ""
		case domain.CodeThreadIDInvalid:
			identity.ThreadID = domain.ID("\x00")
		case domain.CodeUserIDRequired:
			identity.UserID = ""
		case domain.CodeUserIDInvalid:
			identity.UserID = "\x00"
		}
		return identity
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := build(testCase.want).Validate()
			var domainErr *domain.Error
			if err == nil || !errorsAsDomain(err, &domainErr) || domainErr.Code != testCase.want {
				t.Fatalf("Validate() error = %v, want code %q", err, testCase.want)
			}
		})
	}
}

func TestRunRequestIdentity(t *testing.T) {
	request := RunRequest{RunID: "run-1", ThreadID: "thread-1", UserID: "user-1"}
	identity := request.Identity()
	if identity.RunID != domain.ID("run-1") ||
		identity.ThreadID != domain.ID("thread-1") ||
		identity.UserID != "user-1" {
		t.Fatalf("Identity() = %#v", identity)
	}
}

func errorsAsDomain(err error, target **domain.Error) bool {
	if domainErr := (*domain.Error)(nil); errors.As(err, &domainErr) {
		*target = domainErr
		return true
	}
	return false
}
