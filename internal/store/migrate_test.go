package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

// openTestDB opens a throwaway file-backed SQLite database (WAL needs a real
// file; :memory: does not survive database/sql pooling). Caller closes the DB;
// t.TempDir owns the file lifetime.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	})
	return db
}

func tableExists(t *testing.T, ctx context.Context, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type IN ('table','trigger') AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestRunAppFresh(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if err := NewMigrationRunner(db).RunApp(ctx); err != nil {
		t.Fatalf("RunApp: %v", err)
	}
	for _, table := range []string{"users", "auth_sessions", "login_attempts", "claim_tokens"} {
		if !tableExists(t, ctx, db, table) {
			t.Errorf("missing app table %s after RunApp", table)
		}
	}
	v, err := NewMigrationRunner(db).Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Errorf("user_version = %d, want 1", v)
	}

	// Idempotent: re-running on a migrated DB is a no-op.
	if err := NewMigrationRunner(db).RunApp(ctx); err != nil {
		t.Fatalf("second RunApp: %v", err)
	}
	if v2, _ := NewMigrationRunner(db).Version(ctx); v2 != 1 {
		t.Errorf("user_version after re-run = %d, want 1", v2)
	}
}

func TestRunIndexFreshIncludesFTS(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if err := NewMigrationRunner(db).RunIndex(ctx, db); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	for _, table := range []string{"pages", "assets", "links", "blocks", "blocks_fts"} {
		if !tableExists(t, ctx, db, table) {
			t.Errorf("missing index object %s after RunIndex", table)
		}
	}

	// FTS5 sanity (P01 spike): Lane B's chunk-granular FTS has no triggers;
	// writers insert pages/blocks/blocks_fts rows explicitly in one
	// transaction. Porter stemming matches inflections.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO pages(path,path_fold,title,content,secret,owner,updated_at,hash) VALUES(?,?,?,?,0,?,0,?)`,
		"notes/trip.md", "notes/trip.md", "Running trip", "we were running up the hills", "gm", "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO blocks(page_id,ordinal,secret,text) VALUES(?,?,0,?)`,
		"notes/trip.md", 1, "we were running up the hills"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO blocks_fts(page_id,text,ordinal) VALUES(?,?,?)`,
		"notes/trip.md", "we were running up the hills", 1); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM blocks_fts WHERE blocks_fts MATCH 'run'`).Scan(&n); err != nil {
		t.Fatalf("fts match: %v", err)
	}
	if n != 1 {
		t.Errorf("fts match count = %d, want 1", n)
	}
}

func TestRunRespectsExistingVersion(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	// Dir contents are at version 1, so this must be a silent no-op.
	if err := NewMigrationRunner(db).RunApp(ctx); err != nil {
		t.Fatalf("RunApp on current db: %v", err)
	}
}

func TestCollectRejectsBadSQLName(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/app/0001_ok.sql":     {Data: []byte(`CREATE TABLE t(a);`)},
		"migrations/app/notaversion.sql": {Data: []byte(`CREATE TABLE u(a);`)},
	}
	if err := applyFromFS(context.Background(), openTestDB(t), fsys, "migrations/app"); err == nil {
		t.Error("expected error for bad migration file name, got nil")
	}
}

func TestCollectRejectsDuplicateVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/app/0001_one.sql": {Data: []byte(`CREATE TABLE t(a);`)},
		"migrations/app/0001_two.sql": {Data: []byte(`CREATE TABLE u(a);`)},
	}
	if err := applyFromFS(context.Background(), openTestDB(t), fsys, "migrations/app"); err == nil {
		t.Error("expected error for duplicate migration version, got nil")
	}
}

func TestGapDetected(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/app/0001_one.sql":   {Data: []byte(`CREATE TABLE t(a);`)},
		"migrations/app/0003_three.sql": {Data: []byte(`CREATE TABLE u(a);`)},
	}
	if err := applyFromFS(context.Background(), openTestDB(t), fsys, "migrations/app"); err == nil {
		t.Error("expected error for migration gap (missing 0002), got nil")
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	fsys := fstest.MapFS{
		// Second statement re-creates the table: the whole migration,
		// including the user_version bump, must roll back.
		"migrations/app/0001_bad.sql": {Data: []byte(`CREATE TABLE t(a); CREATE TABLE t(a);`)},
	}
	if err := applyFromFS(ctx, db, fsys, "migrations/app"); err == nil {
		t.Fatal("expected error for failing migration, got nil")
	}
	if tableExists(t, ctx, db, "t") {
		t.Error("partial migration survived rollback: table t exists")
	}
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("user_version = %d after failed migration, want 0", v)
	}
}

func TestNonSQLFilesIgnored(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	fsys := fstest.MapFS{
		"migrations/app/README.md":  {Data: []byte("# docs")},
		"migrations/app/0001_a.sql": {Data: []byte(`CREATE TABLE t(a);`)},
	}
	if err := applyFromFS(ctx, db, fsys, "migrations/app"); err != nil {
		t.Fatalf("README.md should be ignored: %v", err)
	}
}
