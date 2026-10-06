package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateKey(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if a == b || a == [32]byte{} {
		t.Fatal("keys must be random and non-zero")
	}
}

func TestLoadOrGenerateKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadOrGenerateKey(dir)
	if err != nil {
		t.Fatalf("load/generate: %v", err)
	}
	k2, err := LoadOrGenerateKey(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if k1 != k2 {
		t.Fatal("key must persist across loads")
	}
	fi, err := os.Stat(filepath.Join(dir, SessionKeyFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("key file must be 0600, got %o", fi.Mode().Perm())
	}
	if _, err := LoadOrGenerateKey(""); err == nil {
		t.Fatal("empty data dir must fail")
	}
}

func TestLoadOrGenerateKeyCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SessionKeyFile), []byte("short"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := LoadOrGenerateKey(dir); err == nil {
		t.Fatal("corrupt key file must fail loudly, never silently regenerate")
	}
}

func TestRotateSessionKeyOverlap(t *testing.T) {
	dir := t.TempDir()

	old, err := LoadOrGenerateKey(dir)
	if err != nil {
		t.Fatalf("init key: %v", err)
	}
	id, _ := NewSessionID()
	exp := time.Now().Add(time.Hour).Unix()
	oldCookie := SignSessionCookie(old, id, "alice", exp)

	current, err := RotateSessionKey(dir)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if current == old {
		t.Fatal("rotated key must differ")
	}
	keys, err := LoadSessionKeys(dir)
	if err != nil {
		t.Fatalf("load keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("want current+prev keys, got %d", len(keys))
	}
	// Pre-rotation cookie still verifies during the grace window.
	if _, _, _, err := ParseAndVerifySessionCookie(keys, oldCookie); err != nil {
		t.Fatalf("old cookie must verify during grace: %v", err)
	}
	// A key signed only by the retired... (sanity: unknown key fails).
	other, _ := GenerateKey()
	if _, _, _, err := ParseAndVerifySessionCookie([][32]byte{other}, oldCookie); err == nil {
		t.Fatal("foreign key must not verify")
	}
}

func TestClaimIssueRedeem(t *testing.T) {
	db := openTestDB(t)
	_, users := mustStores(t, db)
	claims, err := NewClaimStore(db)
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	ctx := context.Background()
	now := time.Now()

	if _, err := CreateUser(ctx, users, "gm", "gm-password-1", true, now.Unix()); err != nil {
		t.Fatalf("create gm: %v", err)
	}
	token, err := claims.IssueClaim(ctx, "gm", "newplayer", false, "table invite", 0, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	// Raw token must not be stored: only its hash.
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM claim_tokens WHERE token_hash = ?`, token).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatal("raw token must never be stored, only its hash")
	}

	u, err := claims.RedeemClaim(ctx, users, token, "newplayer", "player-pw-1", now)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if u.Username != "newplayer" || u.IsGM {
		t.Fatalf("bad redeemed user: %+v", u)
	}
	if _, err := Authenticate(ctx, users, "newplayer", "player-pw-1", now.Unix()); err != nil {
		t.Fatalf("redeemed login: %v", err)
	}
	// Single-use: second redeem fails.
	if _, err := claims.RedeemClaim(ctx, users, token, "newplayer", "player-pw-1", now); err != ErrClaimRedeemed {
		t.Fatalf("double redeem: want ErrClaimRedeemed, got %v", err)
	}
}

func TestClaimExpiryAndMismatch(t *testing.T) {
	db := openTestDB(t)
	_, users := mustStores(t, db)
	claims, err := NewClaimStore(db)
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	ctx := context.Background()
	now := time.Now()

	if _, err := claims.RedeemClaim(ctx, users, "bogus", "x", "password-123", now); err != ErrClaimNotFound {
		t.Fatalf("bogus: want ErrClaimNotFound, got %v", err)
	}

	expired, err := claims.IssueClaim(ctx, "gm", "", false, "", time.Hour, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := claims.RedeemClaim(ctx, users, expired, "late", "password-123", now.Add(2*time.Hour)); err != ErrClaimExpired {
		t.Fatalf("expired: want ErrClaimExpired, got %v", err)
	}

	pinned, err := claims.IssueClaim(ctx, "gm", "pinned", false, "", time.Hour, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := claims.RedeemClaim(ctx, users, pinned, "someone-else", "password-123", now); err == nil {
		t.Fatal("pre-assigned username mismatch must fail")
	}
	if err := claims.RevokeClaim(ctx, pinned); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := claims.RedeemClaim(ctx, users, pinned, "pinned", "password-123", now); err != ErrClaimNotFound {
		t.Fatalf("revoked: want ErrClaimNotFound, got %v", err)
	}
}

func TestClaimOpenRedeemChoosesName(t *testing.T) {
	db := openTestDB(t)
	_, users := mustStores(t, db)
	claims, err := NewClaimStore(db)
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	ctx := context.Background()
	now := time.Now()

	token, err := claims.IssueClaim(ctx, "gm", "", false, "open invite", time.Hour, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	u, err := claims.RedeemClaim(ctx, users, token, "picked-name", "password-123", now)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if u.Username != "picked-name" {
		t.Fatalf("want picked-name, got %q", u.Username)
	}
}

func TestRateLimitWindowRecovery(t *testing.T) {
	db := openTestDB(t)
	_, users := mustStores(t, db)
	limiter, err := NewRateLimiter(db)
	if err != nil {
		t.Fatalf("limiter: %v", err)
	}
	ctx := context.Background()
	base := time.Now().Unix()

	if _, err := CreateUser(ctx, users, "zed", "password-123", false, base); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < MaxLoginAttempts; i++ {
		if err := limiter.Check(ctx, users, "9.9.9.9", "zed", base); err != nil {
			t.Fatalf("attempt %d blocked early: %v", i, err)
		}
		if err := limiter.RecordFailure(ctx, users, "9.9.9.9", "zed", base); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if err := limiter.Check(ctx, users, "9.9.9.9", "zed", base); err == nil {
		t.Fatal("budget exhausted: check must fail")
	}
	// After the window slides past, attempts are allowed again — but the
	// 15-minute account lockout (SQLite-backed) still holds.
	future := base + int64(LoginAttemptWindow.Seconds()) + 1
	if err := limiter.Check(ctx, users, "other-ip", "zed", future); err != ErrAccountLocked {
		t.Fatalf("lockout must persist past window: got %v", err)
	}
	// And past the lockout, with the window clear, login proceeds.
	clear := base + int64((AccountLockout + LoginAttemptWindow + time.Minute).Seconds())
	if err := limiter.Check(ctx, users, "other-ip", "zed", clear); err != nil {
		t.Fatalf("recovered check: %v", err)
	}
}
