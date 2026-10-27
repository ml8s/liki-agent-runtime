package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/google/uuid"
)

// PushConfigStore implements the official A2A push.ConfigStore port.
type PushConfigStore struct {
	db    *sql.DB
	now   func() time.Time
	newID func() (string, error)
}

// PushConfigStoreConfig customizes the durable callback store.
type PushConfigStoreConfig struct {
	Now   func() time.Time
	NewID func() (string, error)
}

// NewPushConfigStore returns the reference durable callback store.
func (db *DB) NewPushConfigStore(config PushConfigStoreConfig) *PushConfigStore {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.NewID == nil {
		config.NewID = func() (string, error) {
			id, err := uuid.NewV7()
			if err != nil {
				return "", fmt.Errorf("generate push config id: %w", err)
			}
			return id.String(), nil
		}
	}
	return &PushConfigStore{db: db.sql, now: config.Now, newID: config.NewID}
}

var _ push.ConfigStore = (*PushConfigStore)(nil)

// Save creates or updates a push notification config within the protocol client's scope.
func (s *PushConfigStore) Save(ctx context.Context, taskID a2a.TaskID, config *a2a.PushConfig) (*a2a.PushConfig, error) {
	if config == nil {
		return nil, fmt.Errorf("%w: push config is required", a2a.ErrInvalidParams)
	}
	owner, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if taskID == "" {
		return nil, fmt.Errorf("%w: task id is required", a2a.ErrInvalidParams)
	}
	if config.ID == "" {
		id, err := s.newID()
		if err != nil {
			return nil, err
		}
		config.ID = id
	}
	config.TaskID = taskID
	config.Tenant = owner.Tenant
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode push config: %w", err)
	}
	now := s.now()
	existingOwner, err := s.findOwner(ctx, taskID, config.ID)
	switch {
	case errors.Is(err, push.ErrPushConfigNotFound):
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO a2a_push_configs
				(task_id, config_id, tenant, principal, owner_key, url, updated_at_unix, config_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			string(taskID), config.ID, owner.Tenant, owner.User, owner.ownerKey, config.URL, now.UnixNano(), string(raw),
		)
		if err != nil {
			return nil, fmt.Errorf("insert push config: %w", err)
		}
	case err != nil:
		return nil, err
	case existingOwner.ownerKey != owner.ownerKey:
		return nil, push.ErrPushConfigNotFound
	default:
		_, err = s.db.ExecContext(ctx, `
			UPDATE a2a_push_configs
			SET tenant = ?, principal = ?, owner_key = ?, url = ?, updated_at_unix = ?, config_json = ?
			WHERE task_id = ? AND config_id = ?`,
			owner.Tenant, owner.User, owner.ownerKey, config.URL, now.UnixNano(), string(raw),
			string(taskID), config.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("update push config: %w", err)
		}
	}
	return copyPushConfig(config)
}

// Get returns one push notification config owned by the authenticated protocol client.
func (s *PushConfigStore) Get(ctx context.Context, taskID a2a.TaskID, configID string) (*a2a.PushConfig, error) {
	config, _, err := s.getOwned(ctx, taskID, configID)
	return config, err
}

func (s *PushConfigStore) getOwned(ctx context.Context, taskID a2a.TaskID, configID string) (*a2a.PushConfig, principal, error) {
	owner, err := principalFromContext(ctx)
	if err != nil {
		return nil, principal{}, err
	}
	if taskID == "" || configID == "" {
		return nil, principal{}, fmt.Errorf("%w: task id and config id are required", a2a.ErrInvalidParams)
	}
	var raw string
	var storedOwner principal
	err = s.db.QueryRowContext(ctx, `
		SELECT tenant, principal, owner_key, config_json
		FROM a2a_push_configs
		WHERE task_id = ? AND config_id = ?`,
		string(taskID), configID,
	).Scan(&storedOwner.Tenant, &storedOwner.User, &storedOwner.ownerKey, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, principal{}, push.ErrPushConfigNotFound
	}
	if err != nil {
		return nil, principal{}, fmt.Errorf("get push config: %w", err)
	}
	if storedOwner.ownerKey != owner.ownerKey {
		return nil, principal{}, push.ErrPushConfigNotFound
	}
	var config a2a.PushConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, principal{}, fmt.Errorf("decode push config: %w", err)
	}
	if config.TaskID == "" {
		config.TaskID = taskID
	}
	return &config, storedOwner, nil
}

// List returns every push notification config for a task in the protocol client's scope.
func (s *PushConfigStore) List(ctx context.Context, taskID a2a.TaskID) ([]*a2a.PushConfig, error) {
	owner, err := principalFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if taskID == "" {
		return nil, fmt.Errorf("%w: task id is required", a2a.ErrInvalidParams)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT config_json
		FROM a2a_push_configs
		WHERE task_id = ? AND owner_key = ?
		ORDER BY config_id`,
		string(taskID), owner.ownerKey,
	)
	if err != nil {
		return nil, fmt.Errorf("list push configs: %w", err)
	}
	configs := make([]*a2a.PushConfig, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan push config: %w", err)
		}
		var config a2a.PushConfig
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			return nil, fmt.Errorf("decode push config: %w", err)
		}
		if config.TaskID == "" {
			config.TaskID = taskID
		}
		configs = append(configs, &config)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate push configs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close push config rows: %w", err)
	}
	return configs, nil
}

// Delete removes one push notification config in the protocol client's scope.
func (s *PushConfigStore) Delete(ctx context.Context, taskID a2a.TaskID, configID string) error {
	owner, err := principalFromContext(ctx)
	if err != nil {
		return err
	}
	if taskID == "" || configID == "" {
		return fmt.Errorf("%w: task id and config id are required", a2a.ErrInvalidParams)
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM a2a_push_configs
		WHERE task_id = ? AND config_id = ? AND owner_key = ?`,
		string(taskID), configID, owner.ownerKey,
	)
	if err != nil {
		return fmt.Errorf("delete push config: %w", err)
	}
	if deleted, err := result.RowsAffected(); err == nil && deleted == 0 {
		return push.ErrPushConfigNotFound
	}
	return nil
}

// DeleteAll removes every push notification config for a task in the protocol client's scope.
func (s *PushConfigStore) DeleteAll(ctx context.Context, taskID a2a.TaskID) error {
	owner, err := principalFromContext(ctx)
	if err != nil {
		return err
	}
	if taskID == "" {
		return fmt.Errorf("%w: task id is required", a2a.ErrInvalidParams)
	}
	_, err = s.db.ExecContext(ctx, `
		DELETE FROM a2a_push_configs
		WHERE task_id = ? AND owner_key = ?`,
		string(taskID), owner.ownerKey,
	)
	if err != nil {
		return fmt.Errorf("delete push configs: %w", err)
	}
	return nil
}

func (s *PushConfigStore) findOwner(ctx context.Context, taskID a2a.TaskID, configID string) (principal, error) {
	var stored principal
	err := s.db.QueryRowContext(ctx, `
		SELECT tenant, principal, owner_key
		FROM a2a_push_configs
		WHERE task_id = ? AND config_id = ?`,
		string(taskID), configID,
	).Scan(&stored.Tenant, &stored.User, &stored.ownerKey)
	if errors.Is(err, sql.ErrNoRows) {
		return principal{}, push.ErrPushConfigNotFound
	}
	if err != nil {
		return principal{}, fmt.Errorf("find push config: %w", err)
	}
	return stored, nil
}

func copyPushConfig(config *a2a.PushConfig) (*a2a.PushConfig, error) {
	if config == nil {
		return nil, nil
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode push config copy: %w", err)
	}
	var copied a2a.PushConfig
	if err := json.Unmarshal(raw, &copied); err != nil {
		return nil, fmt.Errorf("decode push config copy: %w", err)
	}
	return &copied, nil
}
