package auth

import (
	"context"
	"testing"
	"time"
)

// TestDemoFlow is the lane demo: init creates GM → login → cookie works →
// logout kills it → rate-limit trips.
func TestDemoFlow(t *testing.T) {
	db := openTestDB(t)
	sessions, users := mustStores(t, db)
	limiter, err := NewRateLimiter(db)
	if err != nil {
		t.Fatalf("limiter: %v", err)
	}
	ctx := context.Background()
	now := time.Now()

	// init creates GM.
	if _, err := CreateUser(ctx, users, "gm", "gm-password-1", true, now.Unix()); err != nil {
		t.Fatalf("create gm: %v", err)
	}

	// login → cookie works.
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	cfg := DefaultSessionConfig(key)
	sess, _, cookie, err := Login(ctx, users, sessions, limiter, "gm", "gm-password-1", "10.0.0.2", cfg, now)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	got, u, err := AuthenticateRequest(ctx, sessions, users, [][32]byte{key}, cookie, now)
	if err != nil {
		t.Fatalf("request auth: %v", err)
	}
	if got.ID != sess.ID || !u.IsGM {
		t.Fatalf("mismatch: %+v %+v", got, u)
	}

	// logout kills it.
	if err := Logout(ctx, sessions, sess.ID); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := AuthenticateRequest(ctx, sessions, users, [][32]byte{key}, cookie, now); err == nil {
		t.Fatal("logged-out cookie must fail")
	}

	// rate-limit trips: 5 bad passwords, then even the right one is refused.
	for i := 0; i < MaxLoginAttempts; i++ {
		if _, _, _, err := Login(ctx, users, sessions, limiter, "gm", "wrong", "10.0.0.2", cfg, now); err == nil {
			t.Fatalf("bad login %d accepted", i)
		}
	}
	_, _, _, err = Login(ctx, users, sessions, limiter, "gm", "gm-password-1", "10.0.0.2", cfg, now)
	if err != ErrAccountLocked && err != ErrRateLimited {
		t.Fatalf("want lockout/rate-limit, got %v", err)
	}
}

func TestRehashOnLogin(t *testing.T) {
	db := openTestDB(t)
	_, users := mustStores(t, db)
	ctx := context.Background()

	weak, err := hashPasswordWithParams("password-1", ArgonParams{
		Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32,
	})
	if err != nil {
		t.Fatalf("weak hash: %v", err)
	}
	concrete := users.(*UserStoreSQL)
	if err := concrete.Create(ctx, &User{Username: "rehash", PasswordHash: weak, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("seed weak user: %v", err)
	}
	if _, err := Authenticate(ctx, users, "rehash", "password-1", 100); err != nil {
		t.Fatalf("auth: %v", err)
	}
	after, err := users.GetByUsername(ctx, "rehash")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if needs, err := VerifyPassword("password-1", after.PasswordHash); err != nil || needs {
		t.Fatalf("stored hash must be upgraded to defaults: needs=%v err=%v", needs, err)
	}
}

func TestResetPasswordBumpsVersion(t *testing.T) {
	db := openTestDB(t)
	sessions, users := mustStores(t, db)
	ctx := context.Background()
	now := time.Now()

	if _, err := CreateUser(ctx, users, "player", "old-password-1", false, now.Unix()); err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := GenerateKey()
	cfg := DefaultSessionConfig(key)
	sess, _, cookie, err := Login(ctx, users, sessions, nil, "player", "old-password-1", "", cfg, now)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, _, err := AuthenticateRequest(ctx, sessions, users, [][32]byte{key}, cookie, now); err != nil {
		t.Fatalf("pre-reset auth: %v", err)
	}

	if err := ResetPassword(ctx, users, sessions, "player", "new-password-2", now.Unix()+1); err != nil {
		t.Fatalf("reset: %v", err)
	}
	u, _ := users.GetByUsername(ctx, "player")
	if u.SessionVersion != 2 {
		t.Fatalf("version must bump, got %d", u.SessionVersion)
	}
	// Old password dead, old session dead.
	if _, err := Authenticate(ctx, users, "player", "old-password-1", now.Unix()+2); err != ErrInvalidCredentials {
		t.Fatalf("old password: want ErrInvalidCredentials, got %v", err)
	}
	if _, _, err := AuthenticateRequest(ctx, sessions, users, [][32]byte{key}, cookie, now); err == nil {
		t.Fatal("pre-reset cookie must fail")
	}
	_ = sess
	// New password works.
	if _, _, _, err := Login(ctx, users, sessions, nil, "player", "new-password-2", "", cfg, now); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

func TestVersionMismatchRevokesSurvivingRows(t *testing.T) {
	db := openTestDB(t)
	sessions, users := mustStores(t, db)
	ctx := context.Background()
	now := time.Now()

	if _, err := CreateUser(ctx, users, "carol", "password-123", false, now.Unix()); err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := GenerateKey()
	cfg := DefaultSessionConfig(key)
	_, _, cookie, err := Login(ctx, users, sessions, nil, "carol", "password-123", "", cfg, now)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	// Simulate a revoke-all that bumped the version but left the row.
	u, _ := users.GetByUsername(ctx, "carol")
	u.SessionVersion++
	if err := users.Update(ctx, u); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if _, _, err := AuthenticateRequest(ctx, sessions, users, [][32]byte{key}, cookie, now); err != ErrSessionRevoked {
		t.Fatalf("want ErrSessionRevoked, got %v", err)
	}
}

func TestExpiryEnforced(t *testing.T) {
	db := openTestDB(t)
	sessions, users := mustStores(t, db)
	ctx := context.Background()
	now := time.Now()

	if _, err := CreateUser(ctx, users, "dave", "password-123", false, now.Unix()); err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := GenerateKey()
	keys := [][32]byte{key}

	mkSession := func(exp, idle int64) string {
		id, err := sessions.Create(ctx, &Session{
			UserID: "dave", CreatedAt: now.Unix(), ExpiresAt: exp, IdleAt: idle,
			Version: 1, CSRFToken: "c",
		})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		return id
	}
	mkCookie := func(id string, exp int64) string {
		return SignSessionCookie(key, id, "dave", exp)
	}
	future := now.Add(time.Hour).Unix()

	for name, tc := range map[string]struct{ sessExp, sessIdle, cookieExp int64 }{
		"absolute past": {now.Add(-time.Second).Unix(), future, future},
		"idle past":     {future, now.Add(-time.Second).Unix(), future},
		"cookie past":   {future, future, now.Add(-time.Second).Unix()},
	} {
		id := mkSession(tc.sessExp, tc.sessIdle)
		if _, _, err := AuthenticateRequest(ctx, sessions, users, keys, mkCookie(id, tc.cookieExp), now); err != ErrSessionExpired {
			t.Fatalf("%s: want ErrSessionExpired, got %v", name, err)
		}
	}
	id := mkSession(future, future)
	if _, _, err := AuthenticateRequest(ctx, sessions, users, keys, mkCookie(id, future), now); err != nil {
		t.Fatalf("live session rejected: %v", err)
	}
}

func TestSlidingRefresh(t *testing.T) {
	db := openTestDB(t)
	sessions, users := mustStores(t, db)
	ctx := context.Background()
	now := time.Now()

	if _, err := CreateUser(ctx, users, "erin", "password-123", false, now.Unix()); err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := GenerateKey()
	keys := [][32]byte{key}

	// Idle deadline inside the refresh threshold: validation extends it and
	// advances last_seen.
	idle := now.Add(SessionRefreshThreshold - time.Hour).Unix()
	id, err := sessions.Create(ctx, &Session{
		UserID: "erin", CreatedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
		IdleAt: idle, Version: 1, CSRFToken: "c",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cookie := SignSessionCookie(key, id, "erin", now.Add(time.Hour).Unix())
	s, _, err := AuthenticateRequest(ctx, sessions, users, keys, cookie, now)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if s.IdleAt <= idle {
		t.Fatalf("idle deadline must extend: %d -> %d", idle, s.IdleAt)
	}
	lastSeen, err := sessions.(*SessionStoreSQL).LastSeen(ctx, id)
	if err != nil {
		t.Fatalf("last seen: %v", err)
	}
	if lastSeen != now.Unix() {
		t.Fatalf("last_seen must advance, got %d", lastSeen)
	}

	// Fresh idle deadline: no write, no change.
	id2, err := sessions.Create(ctx, &Session{
		UserID: "erin", CreatedAt: now.Unix(), ExpiresAt: now.Add(48 * time.Hour).Unix(),
		IdleAt: now.Add(48 * time.Hour).Unix(), Version: 1, CSRFToken: "c",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s2 := &Session{ID: id2, UserID: "erin", IdleAt: now.Add(48 * time.Hour).Unix()}
	refreshed, err := MaybeRefreshSession(ctx, sessions, s2, now, SessionIdleTimeout)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed {
		t.Fatal("fresh session must not refresh")
	}
}

func TestChangePasswordKeepsOnlyCurrent(t *testing.T) {
	db := openTestDB(t)
	sessions, users := mustStores(t, db)
	ctx := context.Background()
	now := time.Now()

	if _, err := CreateUser(ctx, users, "frank", "old-password-1", false, now.Unix()); err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := GenerateKey()
	keys := [][32]byte{key}
	cfg := DefaultSessionConfig(key)

	_, _, cookieA, err := Login(ctx, users, sessions, nil, "frank", "old-password-1", "", cfg, now)
	if err != nil {
		t.Fatalf("login A: %v", err)
	}
	sessB, _, _, err := Login(ctx, users, sessions, nil, "frank", "old-password-1", "", cfg, now)
	if err != nil {
		t.Fatalf("login B: %v", err)
	}
	idA, _, _, err := ParseAndVerifySessionCookie(keys, cookieA)
	if err != nil {
		t.Fatalf("parse A: %v", err)
	}
	kept, err := ChangePassword(ctx, users, sessions, "frank", idA, "old-password-1", "new-password-2", now)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if kept == nil || kept.ID != idA {
		t.Fatalf("current session must survive: %+v", kept)
	}
	newCookie := SignSessionCookie(key, kept.ID, "frank", kept.ExpiresAt)
	if _, _, err := AuthenticateRequest(ctx, sessions, users, keys, newCookie, now); err != nil {
		t.Fatalf("current session must stay valid: %v", err)
	}
	if _, err := sessions.Get(ctx, sessB.ID); err != ErrSessionNotFound {
		t.Fatalf("other session must die, got %v", err)
	}
}
