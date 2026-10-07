package auth_test

// Lane C verification for the G2 amend (lanes/G2-amend.md): auth behavior
// against a migration-built DB (0001->0002 through the real runner on one
// database). Lane-G rule: these tests verify only. If any fails, file it as
// a finding -- do not change product code to make it pass.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
	_ "modernc.org/sqlite"
)

func openMigratedDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "verify.app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.NewMigrationRunner(db).RunApp(context.Background()); err != nil {
		t.Fatalf("RunApp 0001->0002: %v", err)
	}
	return db, path
}

// legacyShape creates 0001-era tables with a GM row, a live session, a
// locked IP, and a stub claim token, stamped user_version 1. The DDL is
// pinned here (not read from the migration file) so the test stays a true
// legacy fixture even if 0001 is later edited.
func legacyShape(t *testing.T, ctx context.Context, db *sql.DB, now int64) {
	t.Helper()
	for _, ddl := range []string{
		`CREATE TABLE users(name TEXT PRIMARY KEY, role TEXT NOT NULL,
			password_hash TEXT NOT NULL, session_version INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL)`,
		`CREATE TABLE auth_sessions(id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
			expiry INTEGER NOT NULL, last_seen INTEGER NOT NULL,
			revoked INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (user_id) REFERENCES users(name))`,
		`CREATE TABLE login_attempts(ip TEXT PRIMARY KEY,
			fails INTEGER NOT NULL DEFAULT 0, locked_until INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE claim_tokens(token TEXT PRIMARY KEY, slug TEXT NOT NULL,
			expires INTEGER NOT NULL)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("legacy ddl: %v", err)
		}
	}
	hash, err := auth.HashPassword("old-gm-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(name, role, password_hash, session_version, created_at)
		 VALUES('gm', 'gm', ?, 1, ?)`, hash, now); err != nil {
		t.Fatalf("legacy gm: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO auth_sessions(id, user_id, expiry, last_seen, revoked)
		 VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'gm', ?, ?, 0)`,
		now+30*24*3600, now); err != nil {
		t.Fatalf("legacy session: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO login_attempts(ip, fails, locked_until) VALUES('10.9.9.9', 4, ?)`,
		now+900); err != nil {
		t.Fatalf("legacy attempts: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO claim_tokens(token, slug, expires) VALUES('raw', 'slug', ?)`,
		now+3600); err != nil {
		t.Fatalf("legacy claim: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		t.Fatalf("stamp v1: %v", err)
	}
}

func mustAuthStores(t *testing.T, db *sql.DB) (auth.SessionStore, auth.UserStore, *auth.RateLimiter) {
	t.Helper()
	sessions, err := auth.NewSessionStore(db)
	if err != nil {
		t.Fatalf("NewSessionStore on migrated DB: %v", err)
	}
	users, err := auth.NewUserStore(db)
	if err != nil {
		t.Fatalf("NewUserStore on migrated DB: %v", err)
	}
	limiter, err := auth.NewRateLimiter(db)
	if err != nil {
		t.Fatalf("NewRateLimiter on migrated DB: %v", err)
	}
	return sessions, users, limiter
}

// Fresh 0001->0002 DB: stores construct, create+login round-trip works.
func TestMigratedFreshDBLoginRoundTrip(t *testing.T) {
	db, _ := openMigratedDB(t)
	sessions, users, limiter := mustAuthStores(t, db)
	ctx := context.Background()
	now := time.Now()

	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if _, err := auth.CreateUser(ctx, users, "gm", "gm-password-1", true, now.Unix()); err != nil {
		t.Fatalf("create: %v", err)
	}
	sess, u, cookie, err := auth.Login(ctx, users, sessions, limiter,
		"gm", "gm-password-1", "127.0.0.1", auth.DefaultSessionConfig(key), now)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if sess == nil || u == nil || cookie == "" {
		t.Fatal("login returned empty session/user/cookie")
	}
	if _, err := sessions.Get(ctx, sess.ID); err != nil {
		t.Fatalf("session not persisted: %v", err)
	}
	if _, err := auth.Authenticate(ctx, users, "gm", "wrong-pass", now.Unix()); err != auth.ErrInvalidCredentials {
		t.Fatalf("wrong password: want ErrInvalidCredentials, got %v", err)
	}
}

// Upgrade path: legacy GM (role=gm) logs in with the pre-migration password,
// the carried session reads back, and the locked IP stays throttled.
func TestMigratedUpgradeDBGMAndLockoutSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := time.Now().Unix()
	legacyShape(t, ctx, db, now)

	if err := store.NewMigrationRunner(db).RunApp(ctx); err != nil {
		t.Fatalf("RunApp 0001->0002: %v", err)
	}
	sessions, users, limiter := mustAuthStores(t, db)

	got, err := users.GetByUsername(ctx, "gm")
	if err != nil {
		t.Fatalf("gm lookup: %v", err)
	}
	if !got.IsGM {
		t.Error("legacy role=gm did not map to is_gm=1")
	}
	if _, err := auth.Authenticate(ctx, users, "gm", "old-gm-pass", now); err != nil {
		t.Fatalf("legacy GM login with pre-migration password: %v", err)
	}
	carried, err := sessions.Get(ctx, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("carried session unreadable: %v", err)
	}
	if carried.UserID != "gm" {
		t.Errorf("carried session user = %q, want gm", carried.UserID)
	}
	if err := limiter.Check(ctx, users, "10.9.9.9", "anyone", now); err != auth.ErrRateLimited {
		t.Errorf("locked IP: want ErrRateLimited, got %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM claim_tokens`).Scan(&n); err != nil || n != 0 {
		t.Errorf("claim rows = %d, %v; want 0 (documented drop)", n, err)
	}
}

// Account lockout lives in SQLite: it survives close/reopen of the DB.
func TestMigratedDBLockoutSurvivesReopen(t *testing.T) {
	db, path := openMigratedDB(t)
	ctx := context.Background()
	base := time.Now().Unix()
	_, users, limiter := mustAuthStores(t, db)
	if _, err := auth.CreateUser(ctx, users, "zed", "password-123", false, base); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < auth.MaxLoginAttempts; i++ {
		if err := limiter.RecordFailure(ctx, users, "9.9.9.9", "zed", base); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	u, err := users.GetByUsername(ctx, "zed")
	if err != nil || u.LockedUntil <= base {
		t.Fatalf("lockout not set: %+v %v", u, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	_, users2, limiter2 := mustAuthStores(t, db2)
	// Past the 5min window (per-username window rows age out) but inside the
	// 15min account lockout: only users.locked_until can still refuse.
	when := base + int64(auth.LoginAttemptWindow.Seconds()) + 1
	if err := limiter2.Check(ctx, users2, "other-ip", "zed", when); err != auth.ErrAccountLocked {
		t.Fatalf("post-reopen: want ErrAccountLocked, got %v", err)
	}
}
