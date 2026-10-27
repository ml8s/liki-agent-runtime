package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/ml8s/liki-agent-runtime/internal/platform"
	"google.golang.org/adk/v2/session"
	adksession "google.golang.org/adk/v2/session/database"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SQLiteSessionService owns the official ADK relational session service and
// its underlying database handle. Runtime composition closes it on shutdown.
type SQLiteSessionService struct {
	service session.Service
	db      *sql.DB
}

// OpenSQLiteSessionService opens the official ADK relational session service
// backed by SQLite. The service and its schema come from ADK; this adapter only
// configures a safe SQLite deployment.
func OpenSQLiteSessionService(path string) (*SQLiteSessionService, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create ADK session directory: %w", err)
			}
		}
	}
	gormDB, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Warn),
		NowFunc: time.Now().UTC,
	})
	if err != nil {
		return nil, fmt.Errorf("open ADK session sqlite: %w", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("get ADK session sqlite handle: %w", err)
	}
	// One connection avoids SQLITE_BUSY for this single-process topology. ADK's
	// transactional session operations remain in charge of correctness.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("configure ADK session sqlite (%s): %w", pragma, err)
		}
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("restrict ADK session permissions: %w", err)
		}
		for _, sidecar := range []string{path + "-wal", path + "-shm"} {
			if err := os.Chmod(sidecar, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
				_ = sqlDB.Close()
				return nil, fmt.Errorf("restrict ADK session sidecar permissions: %w", err)
			}
		}
	}
	service, err := adksession.NewSessionServiceFromDB(gormDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := adksession.AutoMigrate(service); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate ADK sessions: %w", err)
	}
	return &SQLiteSessionService{service: service, db: sqlDB}, nil
}

// SessionService returns the official ADK service.
func (s *SQLiteSessionService) SessionService() session.Service {
	if s == nil {
		return nil
	}
	return s.service
}

// CheckHealth reports whether ADK session storage is reachable.
func (s *SQLiteSessionService) CheckHealth(ctx context.Context) platform.DependencyHealth {
	health := platform.DependencyHealth{Name: "adk_session_state", OK: true}
	if s == nil || s.db == nil {
		health.OK = false
		health.Detail = "closed"
		return health
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		health.OK = false
		health.Detail = err.Error()
	}
	return health
}

// Close releases the underlying database.
func (s *SQLiteSessionService) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
