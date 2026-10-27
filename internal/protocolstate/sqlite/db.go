// Package sqlite stores standard A2A protocol state separately from audit
// evidence. Database technology remains confined to this adapter.
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

	// Blank import registers the pure-Go SQLite database/sql driver used only
	// through database/sql in this adapter.
	_ "github.com/glebarez/sqlite"
	"github.com/ml8s/agent-runtime/internal/platform"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB owns the durable A2A protocol state connection. One connection serializes
// writers; optimistic task versions and explicit transactions preserve
// correctness if the deployment later shares this adapter across processes.
type DB struct {
	sql *sql.DB
}

// Open opens and migrates the protocol-state database.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create protocol state directory: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open protocol state sqlite: %w", err)
	}
	// One connection makes this single-process adapter deterministic and
	// avoids SQLITE_BUSY between reads and writes. Standard optimistic task
	// versions remain in the schema for multi-process evolution.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := configure(db, path); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("restrict protocol state permissions: %w", err)
		}
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{sql: db}, nil
}

func configure(db *sql.DB, path string) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("configure protocol state sqlite (%s): %w", pragma, err)
		}
	}
	if path == ":memory:" {
		return nil
	}
	// WAL sidecars inherit SQLite's creation mode, not the main database's
	// post-open chmod, so restrict them immediately after WAL activation.
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(sidecar, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restrict protocol state sidecar permissions: %w", err)
		}
	}
	return nil
}

func migrate(db *sql.DB) error {
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("mount protocol state migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		return fmt.Errorf("create protocol state migration provider: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("run protocol state migrations: %w", err)
	}
	return nil
}

// CheckHealth reports whether the protocol state database is reachable.
func (db *DB) CheckHealth(ctx context.Context) platform.DependencyHealth {
	if db == nil || db.sql == nil {
		return platform.DependencyHealth{Name: "a2a_protocol_state", OK: false, Detail: "closed"}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := db.sql.PingContext(ctx); err != nil {
		return platform.DependencyHealth{Name: "a2a_protocol_state", OK: false, Detail: err.Error()}
	}
	return platform.DependencyHealth{Name: "a2a_protocol_state", OK: true}
}

// Close releases the database connection.
func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}
