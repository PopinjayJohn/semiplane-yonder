package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
)

//go:embed migrations/index/*.sql migrations/app/*.sql
var migrationFS embed.FS

// MigrationRunner handles database migrations using embedded SQL files.
// Lane B owns migrations/{index,app}/*.sql contents; Lane E2 owns this runner.
// Two targets, independent user_version streams (P04):
//   - migrations/index/ → <name>.index.db (disposable; applied at reindex build
//     time to a temp DB, then rename).
//   - migrations/app/ → <name>.app.db (precious; applied at serve/migrate time,
//     never rebuilt).
type MigrationRunner struct {
	db *sql.DB
}

// NewMigrationRunner creates a new migration runner bound to one database.
// For the app DB pass Store.AppDB(); for reindex builds pass the temp index DB.
func NewMigrationRunner(db *sql.DB) *MigrationRunner {
	return &MigrationRunner{db: db}
}

// Run applies all pending app migrations to the bound database.
// Deprecated: use RunApp. Kept for Phase-0 call sites; identical to RunApp.
func (r *MigrationRunner) Run(ctx context.Context) error {
	return r.RunApp(ctx)
}

// RunApp applies pending migrations/app/*.sql to the bound (app) database.
// Uses PRAGMA user_version for version tracking.
func (r *MigrationRunner) RunApp(ctx context.Context) error {
	return r.applyDir(ctx, r.db, "migrations/app")
}

// RunIndex applies pending migrations/index/*.sql to the given index database.
// Used by the reindex path (Lane B) against a temp DB before rename.
func (r *MigrationRunner) RunIndex(ctx context.Context, indexDB *sql.DB) error {
	return r.applyDir(ctx, indexDB, "migrations/index")
}

func (r *MigrationRunner) applyDir(ctx context.Context, db *sql.DB, dir string) error {
	current, err := currentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	entries, err := migrationFS.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		version := parseVersion(name)
		if version <= current {
			continue
		}

		sqlBytes, err := migrationFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s/%s: %w", dir, name, err)
		}

		if _, err := db.ExecContext(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s/%s: %w", dir, name, err)
		}

		if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			return fmt.Errorf("set user_version %d: %w", version, err)
		}
		current = version
	}

	return nil
}

func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
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
