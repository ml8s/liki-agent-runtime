// Package sqlite is the append-only audit persistence adapter. Database
// technology is confined to this package.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/ml8s/liki-agent-runtime/internal/platform"
	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB owns the SQLite connection used for audit persistence.
type DB struct {
	gorm *gorm.DB
}

// Open opens the audit database, applies migrations, and configures SQLite for
// append-only evidence storage.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create sqlite directory: %w", err)
		}
	}
	gormDB, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			if sqlDB, closeErr := gormDB.DB(); closeErr == nil {
				_ = sqlDB.Close()
			}
			return nil, fmt.Errorf("restrict sqlite permissions: %w", err)
		}
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("get sqlite handle: %w", err)
	}
	if _, err := sqlDB.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return nil, fmt.Errorf("enable sqlite WAL: %w", err)
	}
	// Audit events are durable evidence: FULL synchronous commits every
	// transaction to disk before acknowledging it. Single-process scale keeps
	// the write cost acceptable while WAL preserves crash atomicity.
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := sqlDB.Exec(pragma); err != nil {
			return nil, fmt.Errorf("configure sqlite (%s): %w", pragma, err)
		}
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	if err := migrateAuditSchema(sqlDB); err != nil {
		return nil, err
	}
	db := &DB{gorm: gormDB}
	return db, nil
}

// GORM returns the underlying GORM handle for repository construction.
func (db *DB) GORM() *gorm.DB {
	return db.gorm
}

// Close releases the database connection.
func (db *DB) Close() error {
	sqlDB, err := db.gorm.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// CheckHealth reports whether the audit database is reachable.
func (db *DB) CheckHealth(ctx context.Context) platform.DependencyHealth {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	sqlDB, err := db.gorm.DB()
	if err != nil {
		return platform.DependencyHealth{Name: "sqlite", OK: false, Detail: err.Error()}
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return platform.DependencyHealth{Name: "sqlite", OK: false, Detail: err.Error()}
	}
	return platform.DependencyHealth{Name: "sqlite", OK: true}
}

func migrateAuditSchema(sqlDB *sql.DB) error {
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("mount migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, migrations)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
