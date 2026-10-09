package auth

// Lane L verification harness (Phase 4, G4): login_attempts enforcement —
// 5 fails / 5min per IP + per account, 15min account lockout — through the
// full Login flow, including close/reopen restart persistence (spec §7,
// pitfalls: "Lockout state lives in SQLite, not memory — restarts must not
// clear it").
//
// Tests only: no product-code change. Any failure here is FILED as a
// finding against the auth surface, never fixed by reshaping product code
// in this lane.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openHarnessDB opens a file-backed app DB at a returned path so the test
// can close and reopen it mid-scenario (restart persistence).
func openHarnessDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "harness.app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open harness db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureAuthSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return db, path
}

func reopenHarnessDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen harness db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func harnessLogin(t *testing.T, ctx context.Context, users UserStore, sessions SessionStore,
	limiter *RateLimiter, username, password, ip string, now time.Time) error {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	_, _, _, err = Login(ctx, users, sessions, limiter, username, password, ip, DefaultSessionConfig(key), now)
	return err
}

// Full Login flow: 5 bad passwords trip the budget, the 6th attempt — even
// with the RIGHT password — is refused, and the lockout survives a
// close/reopen of the DB.
func TestLaneLLockoutSurvivesRestartThroughLogin(t *testing.T) {
	db, path := openHarnessDB(t)
	sessions, users := mustStores(t, db)
	limiter, err := NewRateLimiter(db)
	if err != nil {
		t.Fatalf("limiter: %v", err)
	}
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	if _, err := CreateUser(ctx, users, "zed", "password-123", false, now.Unix()); err != nil {
		t.Fatalf("create: %v", err)
	}

	for i := 0; i < MaxLoginAttempts; i++ {
		err := harnessLogin(t, ctx, users, sessions, limiter, "zed", "wrong", "9.9.9.9", now)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("bad login %d: want ErrInvalidCredentials, got %v", i, err)
		}
	}
	u, err := users.GetByUsername(ctx, "zed")
	if err != nil || u.LockedUntil <= now.Unix() {
		t.Fatalf("15min account lockout not set after 5 fails: %+v %v", u, err)
	}
	if want := now.Unix() + int64(AccountLockout.Seconds()); u.LockedUntil != want {
		t.Fatalf("LockedUntil = %d, want exactly now+15min = %d", u.LockedUntil, want)
	}

	// Restart: close and reopen the DB, rebuild stores over the same file.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2 := reopenHarnessDB(t, path)
	sessions2, users2 := mustStores(t, db2)
	limiter2, err := NewRateLimiter(db2)
	if err != nil {
		t.Fatalf("limiter2: %v", err)
	}

	// Correct password from a fresh IP, inside the lockout: still refused.
	err = harnessLogin(t, ctx, users2, sessions2, limiter2, "zed", "password-123", "10.1.2.3", now)
	if !errors.Is(err, ErrAccountLocked) && !errors.Is(err, ErrRateLimited) {
		t.Fatalf("post-restart login: want ErrAccountLocked/ErrRateLimited, got %v", err)
	}
	// Past the 15min lockout (and past the 5min window): login works again
	// and the success clears the lockout row.
	later := now.Add(AccountLockout + LoginAttemptWindow + time.Minute)
	if err := harnessLogin(t, ctx, users2, sessions2, limiter2, "zed", "password-123", "10.1.2.3", later); err != nil {
		t.Fatalf("post-expiry login: %v", err)
	}
	u2, _ := users2.GetByUsername(ctx, "zed")
	if u2.LockedUntil != 0 || u2.FailedLogins != 0 {
		t.Fatalf("successful login must clear lockout counters: %+v", u2)
	}
}

// Per-IP budget: 5 failures from one IP (unknown users, so no account row
// exists) throttle that IP, and the window rows survive a restart.
func TestLaneLIPBudgetSurvivesRestart(t *testing.T) {
	db, path := openHarnessDB(t)
	sessions, users := mustStores(t, db)
	limiter, err := NewRateLimiter(db)
	if err != nil {
		t.Fatalf("limiter: %v", err)
	}
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	for i := 0; i < MaxLoginAttempts; i++ {
		err := harnessLogin(t, ctx, users, sessions, limiter, "ghost", "wrong", "9.9.9.9", now)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("bad login %d: want ErrInvalidCredentials, got %v", i, err)
		}
	}
	if err := limiter.Check(ctx, users, "9.9.9.9", "ghost", now.Unix()); err != ErrRateLimited {
		t.Fatalf("IP budget exhausted: want ErrRateLimited, got %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2 := reopenHarnessDB(t, path)
	_, users2 := mustStores(t, db2)
	limiter2, err := NewRateLimiter(db2)
	if err != nil {
		t.Fatalf("limiter2: %v", err)
	}
	// Same IP, different account, post-restart: the IP window still holds.
	if err := limiter2.Check(ctx, users2, "9.9.9.9", "anyone-else", now.Unix()); err != ErrRateLimited {
		t.Fatalf("post-restart IP check: want ErrRateLimited, got %v", err)
	}
	// A different IP is unaffected.
	if err := limiter2.Check(ctx, users2, "10.0.0.1", "anyone-else", now.Unix()); err != nil {
		t.Fatalf("clean IP must pass, got %v", err)
	}
}

// Per-account isolation: alice's failures never spend bob's budget when the
// IPs differ.
func TestLaneLAccountBudgetsAreIndependent(t *testing.T) {
	db := openTestDB(t)
	sessions, users := mustStores(t, db)
	limiter, err := NewRateLimiter(db)
	if err != nil {
		t.Fatalf("limiter: %v", err)
	}
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	for _, name := range []string{"alice", "bob"} {
		if _, err := CreateUser(ctx, users, name, "password-123", false, now.Unix()); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	for i := 0; i < MaxLoginAttempts; i++ {
		_ = harnessLogin(t, ctx, users, sessions, limiter, "alice", "wrong", "10.9.9.1", now)
	}
	if err := harnessLogin(t, ctx, users, sessions, limiter, "bob", "password-123", "10.9.9.2", now); err != nil {
		t.Fatalf("unrelated account+IP must log in, got %v", err)
	}
}
