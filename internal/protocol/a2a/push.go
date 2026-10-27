package a2a

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	observability "github.com/ml8s/agent-runtime/internal/observability"
)

// validatingPushConfigStore enforces the callback policy before a webhook can
// enter storage. The SDK sender additionally validates resolved addresses at
// dial time, which remains the authoritative SSRF boundary.
type validatingPushConfigStore struct {
	delegate push.ConfigStore
	metrics  observability.A2AMetrics
}

func newMeasuredPushConfigStore(delegate push.ConfigStore, metrics observability.A2AMetrics) push.ConfigStore {
	return validatingPushConfigStore{delegate: delegate, metrics: metrics}
}

func observePushConfig(metrics observability.A2AMetrics, operation string, err error) {
	if metrics == nil {
		return
	}
	status := "success"
	switch {
	case errors.Is(err, push.ErrPushConfigNotFound), errors.Is(err, a2a.ErrTaskNotFound):
		status = "not_found"
	case errors.Is(err, a2a.ErrInvalidParams), errors.Is(err, a2a.ErrInvalidRequest):
		status = "invalid"
	case err != nil:
		status = "error"
	}
	metrics.ObservePushConfig(operation, status)
}

type measuredPushSender struct {
	delegate push.Sender
	metrics  observability.A2AMetrics
}

func withMeasuredPushSender(delegate push.Sender, metrics observability.A2AMetrics) push.Sender {
	return measuredPushSender{delegate: delegate, metrics: metrics}
}

func (s measuredPushSender) SendPush(ctx context.Context, config *a2a.PushConfig, event a2a.Event) error {
	started := time.Now()
	err := s.delegate.SendPush(ctx, config, event)
	if s.metrics != nil {
		status := "success"
		if err != nil {
			status = "error"
		}
		s.metrics.ObservePushDelivery(status, time.Since(started))
	}
	return err
}

func (s validatingPushConfigStore) Save(
	ctx context.Context,
	taskID a2a.TaskID,
	config *a2a.PushConfig,
) (*a2a.PushConfig, error) {
	if err := validatePushConfig(taskID, config); err != nil {
		observePushConfig(s.metrics, "save", err)
		return nil, err
	}
	saved, err := s.delegate.Save(ctx, taskID, config)
	observePushConfig(s.metrics, "save", err)
	return saved, err
}

func (s validatingPushConfigStore) Get(
	ctx context.Context,
	taskID a2a.TaskID,
	configID string,
) (*a2a.PushConfig, error) {
	config, err := s.delegate.Get(ctx, taskID, configID)
	observePushConfig(s.metrics, "get", err)
	return config, err
}

func (s validatingPushConfigStore) List(
	ctx context.Context,
	taskID a2a.TaskID,
) ([]*a2a.PushConfig, error) {
	configs, err := s.delegate.List(ctx, taskID)
	observePushConfig(s.metrics, "list", err)
	return configs, err
}

func (s validatingPushConfigStore) Delete(
	ctx context.Context,
	taskID a2a.TaskID,
	configID string,
) error {
	err := s.delegate.Delete(ctx, taskID, configID)
	observePushConfig(s.metrics, "delete", err)
	return err
}

func (s validatingPushConfigStore) DeleteAll(ctx context.Context, taskID a2a.TaskID) error {
	err := s.delegate.DeleteAll(ctx, taskID)
	observePushConfig(s.metrics, "delete_all", err)
	return err
}

func validatePushConfig(taskID a2a.TaskID, config *a2a.PushConfig) error {
	if taskID == "" || config == nil || config.URL == "" {
		return fmt.Errorf("%w: task id and push URL are required", a2a.ErrInvalidParams)
	}
	parsed, err := url.Parse(config.URL)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Opaque != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf("%w: push URL must be an HTTPS URL without credentials or fragment", a2a.ErrInvalidParams)
	}
	if config.Auth == nil {
		return nil
	}
	switch strings.ToLower(config.Auth.Scheme) {
	case "basic", "bearer":
		if config.Auth.Credentials == "" {
			return fmt.Errorf("%w: push auth credentials are required", a2a.ErrInvalidParams)
		}
	default:
		return fmt.Errorf("%w: unsupported push auth scheme", a2a.ErrInvalidParams)
	}
	return nil
}
