// Package sqlite is the append-only audit persistence adapter. Database
// technology is confined to this package.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/ml8s/agent-runtime/internal/platform"
	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB owns the SQLite connections used for audit persistence: a read pool and
// one dedicated write connection owned by the single audit writer.
type DB struct {
	gorm  *gorm.DB
	write *gorm.DB
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
	gormDB, err := openGormAudit(path)
	if err != nil {
		return nil, err
	}
	readDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("get sqlite handle: %w", err)
	}
	if err := migrateAuditSchema(readDB); err != nil {
		return nil, err
	}

	// An in-memory SQLite database lives per connection, so it must keep one
	// shared handle; the audit writer then serializes on that single pool.
	writeDB := gormDB
	if path != ":memory:" {
		writeDB, err = openGormAudit(path)
		if err != nil {
			return nil, errors.Join(err, readDB.Close())
		}
		writeSQLDB, writeErr := writeDB.DB()
		if writeErr != nil {
			return nil, errors.Join(fmt.Errorf("get sqlite write handle: %w", writeErr), readDB.Close())
		}
		// One write connection makes the writer the process's only append path
		// and removes SQLITE_BUSY contention between our own connections.
		writeSQLDB.SetMaxOpenConns(1)
		writeSQLDB.SetMaxIdleConns(1)
	}
	return &DB{gorm: gormDB, write: writeDB}, nil
}

func openGormAudit(path string) (*gorm.DB, error) {
	gormDB, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
		// Prepared statements remove per-append SQL planning from the audit
		// hot path. Single-row appends need no wrapping transaction.
		PrepareStmt:            true,
		SkipDefaultTransaction: true,
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
	return gormDB, nil
}

// GORM returns the read-pool GORM handle for repository construction.
func (db *DB) GORM() *gorm.DB {
	return db.gorm
}

// writeGORM returns the dedicated single-connection write handle. It stays
// package-private so every append flows through the audit writer.
func (db *DB) writeGORM() *gorm.DB {
	if db.write != nil {
		return db.write
	}
	return db.gorm
}

// Close releases the database connections.
func (db *DB) Close() error {
	var writeErr error
	if db.write != nil && db.write != db.gorm {
		if writeSQLDB, err := db.write.DB(); err != nil {
			writeErr = err
		} else {
			writeErr = writeSQLDB.Close()
		}
	}
	sqlDB, err := db.gorm.DB()
	if err != nil {
		return errors.Join(writeErr, err)
	}
	return errors.Join(writeErr, sqlDB.Close())
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
