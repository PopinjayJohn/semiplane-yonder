package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
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
	return applyFromFS(ctx, indexDB, migrationFS, "migrations/index")
}

// Version returns the current PRAGMA user_version of the bound database.
func (r *MigrationRunner) Version(ctx context.Context) (int, error) {
	return currentVersion(ctx, r.db)
}

// migrationName matches the only accepted file shape: NNNN_description.sql.
// Non-.sql files (e.g. README.md) are ignored so Lane B can document the dir;
// a *.sql file that does not match is a hard error, never a silent skip.
var migrationName = regexp.MustCompile(`^(\d+)_.*\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

func (r *MigrationRunner) applyDir(ctx context.Context, db *sql.DB, dir string) error {
	return applyFromFS(ctx, db, migrationFS, dir)
}

func applyFromFS(ctx context.Context, db *sql.DB, fsys fs.FS, dir string) error {
	migrations, err := collectMigrations(fsys, dir)
	if err != nil {
		return err
	}

	current, err := currentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if m.version != current+1 {
			return fmt.Errorf("migration gap in %s: have version %d, next file is %s (missing version %d)",
				dir, current, m.name, current+1)
		}
		if err := applyOne(ctx, db, dir, m); err != nil {
			return err
		}
		current = m.version
	}

	return nil
}

// applyOne runs a single migration plus its user_version bump inside one
// transaction, so a half-applied migration never advances the version and a
// retry replays it from scratch.
func applyOne(ctx context.Context, db *sql.DB, dir string, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s/%s: %w", dir, m.name, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("apply migration %s/%s: %w", dir, m.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("set user_version %d for %s/%s: %w", m.version, dir, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s/%s: %w", dir, m.name, err)
	}
	committed = true
	return nil
}

func collectMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %s: %w", dir, err)
	}

	var out []migration
	seen := map[int]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		match := migrationName.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("bad migration name %s/%s: want NNNN_description.sql", dir, name)
		}
		var version int
		if _, err := fmt.Sscanf(match[1], "%d", &version); err != nil || version < 1 {
			return nil, fmt.Errorf("bad migration version %s/%s: want NNNN_description.sql", dir, name)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d in %s: %s and %s", version, dir, prev, name)
		}
		seen[version] = name

		sqlBytes, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration %s/%s: %w", dir, name, err)
		}
		out = append(out, migration{version: version, name: name, sql: string(sqlBytes)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}

// ErrNoMigrations is returned when no migration files are found.
var ErrNoMigrations = errors.New("no migration files found")
