package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// MigrationRunner handles database migrations using embedded SQL files.
// Lane B owns migrations/*.sql content; Lane E2 owns this runner.
type MigrationRunner struct {
	db *sql.DB
}

// NewMigrationRunner creates a new migration runner.
func NewMigrationRunner(db *sql.DB) *MigrationRunner {
	return &MigrationRunner{db: db}
}

// Run applies all pending migrations.
// Uses PRAGMA user_version for version tracking.
func (r *MigrationRunner) Run(ctx context.Context) error {
	current, err := r.currentVersion(ctx)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		version := parseVersion(entry.Name())
		if version <= current {
			continue
		}

		sqlBytes, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		if _, err := r.db.ExecContext(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}

		if _, err := r.db.ExecContext(ctx, "PRAGMA user_version = ?", version); err != nil {
			return fmt.Errorf("set user_version %d: %w", version, err)
		}
	}

	return nil
}

func (r *MigrationRunner) currentVersion(ctx context.Context) (int, error) {
	var version int
	err := r.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}

func parseVersion(filename string) int {
	// Expect format: 0001_description.sql
	var v int
	_, err := fmt.Sscanf(filename, "%d_", &v)
	if err != nil {
		return 0
	}
	return v
}

// ErrNoMigrations is returned when no migration files are found.
var ErrNoMigrations = errors.New("no migration files found")
