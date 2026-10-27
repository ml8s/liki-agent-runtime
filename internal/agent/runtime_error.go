package agent

import (
	"context"
	"errors"

	"github.com/ml8s/agent-runtime/internal/domain"
)

func runtimeError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		return domainErr
	}
	if errors.Is(err, context.Canceled) {
		return domain.NewError(domain.CodeRuntimeCancelled, "agent run was cancelled", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.NewError(domain.CodeRuntimeTimeout, "agent run exceeded its deadline", err)
	}
	return domain.NewError(domain.CodeRuntimeFailed, "agent runtime failed", err)
}
