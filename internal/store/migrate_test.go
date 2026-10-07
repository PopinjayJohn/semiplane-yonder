package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/semiplane/yonder/internal/auth"
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
	if v != 2 {
		t.Errorf("user_version = %d, want 2", v)
	}

	// Idempotent: re-running on a migrated DB is a no-op.
	if err := NewMigrationRunner(db).RunApp(ctx); err != nil {
		t.Fatalf("second RunApp: %v", err)
	}
	if v2, _ := NewMigrationRunner(db).Version(ctx); v2 != 2 {
		t.Errorf("user_version after re-run = %d, want 2", v2)
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
	// A DB already at the latest version is a silent no-op.
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
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

// applyApp0001Only builds a 0001-era DB: the real 0001 file applied alone
// (user_version 1), so upgrade tests start from the exact legacy shape.
func applyApp0001Only(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	raw, err := fs.ReadFile(migrationFS, "migrations/app/0001_app_auth.sql")
	if err != nil {
		t.Fatalf("read 0001: %v", err)
	}
	one := fstest.MapFS{"migrations/app/0001_app_auth.sql": {Data: raw}}
	if err := applyFromFS(ctx, db, one, "migrations/app"); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
}

// TestApp0002UpgradeFrom0001 covers the amend path (b): a 0001-era DB holding
// a GM user + sessions advances to 2 with rows reconciled per lanes/G2-amend.md,
// and EnsureAuthSchema then composes on the same DB (the G2 serve crash).
func TestApp0002UpgradeFrom0001(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	applyApp0001Only(t, ctx, db)

	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(name, role, password_hash, session_version, created_at)
		 VALUES('gm', 'gm', 'phc-gm', 3, ?), ('alice', 'player', 'phc-alice', 1, ?)`,
		now, now); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO auth_sessions(id, user_id, expiry, last_seen, revoked)
		 VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'gm', ?, ?, 0)`,
		now+30*24*3600, now-100); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO login_attempts(ip, fails, locked_until)
		 VALUES('10.0.0.9', 4, ?), ('10.0.0.10', 2, 0)`,
		now+900); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO claim_tokens(token, slug, expires) VALUES('rawtoken', 'some-slug', ?)`,
		now+3600); err != nil {
		t.Fatalf("seed claims: %v", err)
	}

	if err := NewMigrationRunner(db).RunApp(ctx); err != nil {
		t.Fatalf("RunApp 0001->0002: %v", err)
	}
	if v, _ := NewMigrationRunner(db).Version(ctx); v != 2 {
		t.Fatalf("user_version = %d, want 2", v)
	}

	// users: role mapped, row carried.
	var isGM, ver, updated int
	var hash string
	var lastLogin, failed, locked int64
	if err := db.QueryRowContext(ctx,
		`SELECT is_gm, password_hash, session_version, updated_at,
		 last_login, failed_logins, locked_until FROM users WHERE name = 'gm'`).
		Scan(&isGM, &hash, &ver, &updated, &lastLogin, &failed, &locked); err != nil {
		t.Fatalf("gm row: %v", err)
	}
	if isGM != 1 || hash != "phc-gm" || ver != 3 || int64(updated) != now {
		t.Errorf("gm mangled: is_gm=%d hash=%q ver=%d updated=%d", isGM, hash, ver, updated)
	}
	if lastLogin != 0 || failed != 0 || locked != 0 {
		t.Errorf("gm throttle defaults wrong: %d/%d/%d", lastLogin, failed, locked)
	}
	if err := db.QueryRowContext(ctx, `SELECT is_gm FROM users WHERE name = 'alice'`).Scan(&isGM); err != nil || isGM != 0 {
		t.Errorf("alice is_gm = %d, %v; want 0", isGM, err)
	}

	// auth_sessions: live row carried with documented defaults.
	var created, expires, idle, seen int64
	var csrf string
	var sver, revoked int
	if err := db.QueryRowContext(ctx,
		`SELECT created_at, expires_at, idle_at, last_seen, csrf_token, version, revoked
		 FROM auth_sessions WHERE id = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`).
		Scan(&created, &expires, &idle, &seen, &csrf, &sver, &revoked); err != nil {
		t.Fatalf("session row: %v", err)
	}
	if created != now-100 || seen != now-100 || expires != now+30*24*3600 || idle != now-100+86400 {
		t.Errorf("session times wrong: created=%d expires=%d idle=%d seen=%d",
			created, expires, idle, seen)
	}
	if csrf != "" || sver != 1 || revoked != 0 {
		t.Errorf("session defaults wrong: csrf=%q version=%d revoked=%d", csrf, sver, revoked)
	}

	// login_attempts: locked IP re-seeded with 5 fresh rows; plain counters reset.
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM login_attempts WHERE ip = '10.0.0.9' AND success = 0`).Scan(&n); err != nil || n != 5 {
		t.Errorf("locked ip rows = %d, %v; want 5", n, err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM login_attempts WHERE ip = '10.0.0.10'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("unlocked ip rows = %d, %v; want 0", n, err)
	}

	// claim_tokens: dropped (unmigratable hash), recreated empty in new shape.
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM claim_tokens`).Scan(&n); err != nil || n != 0 {
		t.Errorf("claim rows = %d, %v; want 0", n, err)
	}

	// The G2 crash shape: EnsureAuthSchema on the migrated DB must succeed.
	if err := auth.EnsureAuthSchema(db); err != nil {
		t.Fatalf("EnsureAuthSchema after 0002: %v", err)
	}
}

// TestApp0002MatchesEnsureAuthSchema pins the 0002 contract: migration-built
// tables are column-identical to EnsureAuthSchema-built ones, so both DDL
// sources compose on one DB in either order.
func TestApp0002MatchesEnsureAuthSchema(t *testing.T) {
	ctx := context.Background()
	migrated := openTestDB(t)
	if err := NewMigrationRunner(migrated).RunApp(ctx); err != nil {
		t.Fatalf("RunApp: %v", err)
	}
	ensured := openTestDB(t)
	if err := auth.EnsureAuthSchema(ensured); err != nil {
		t.Fatalf("EnsureAuthSchema: %v", err)
	}

	cols := func(db *sql.DB, table string) []string {
		rows, err := db.QueryContext(ctx,
			`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("table_info %s: %v", table, err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var name, typ string
			var notnull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			dv := ""
			if dflt.Valid {
				dv = strings.TrimSpace(dflt.String)
			}
			out = append(out, name+"|"+strings.ToUpper(strings.TrimSpace(typ))+
				"|"+itoa(notnull)+"|"+dv+"|"+itoa(pk))
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows %s: %v", table, err)
		}
		return out
	}
	idx := func(db *sql.DB, table string) []string {
		rows, err := db.QueryContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL ORDER BY name`, table)
		if err != nil {
			t.Fatalf("indexes %s: %v", table, err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan idx %s: %v", table, err)
			}
			out = append(out, name)
		}
		return out
	}
	for _, table := range []string{"users", "auth_sessions", "login_attempts", "claim_tokens"} {
		mc, ec := cols(migrated, table), cols(ensured, table)
		if strings.Join(mc, "\n") != strings.Join(ec, "\n") {
			t.Errorf("table %s diverges:\n migrated: %q\n ensured:  %q", table, mc, ec)
		}
		mi, ei := idx(migrated, table), idx(ensured, table)
		if strings.Join(mi, ",") != strings.Join(ei, ",") {
			t.Errorf("table %s indexes diverge: migrated %q ensured %q", table, mi, ei)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}
