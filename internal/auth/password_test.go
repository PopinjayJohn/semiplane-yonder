package auth

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.app.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsureAuthSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return db
}

func mustStores(t *testing.T, db *sql.DB) (SessionStore, UserStore) {
	t.Helper()
	sessions, err := NewSessionStore(db)
	if err != nil {
		t.Fatalf("new session store: %v", err)
	}
	users, err := NewUserStore(db)
	if err != nil {
		t.Fatalf("new user store: %v", err)
	}
	return sessions, users
}

func TestHashVerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("unexpected PHC prefix: %s", hash)
	}
	needsRehash, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if needsRehash {
		t.Fatal("fresh default hash must not need rehash")
	}
}

func TestVerifyWrongPassword(t *testing.T) {
	hash, err := HashPassword("right-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := VerifyPassword("wrong-password", hash); err != ErrInvalidCredentials {
		t.Fatalf("want ErrInvalidCredentials, got %v", err)
	}
}

func TestVerifyMalformedHashes(t *testing.T) {
	bad := []string{
		"",
		"not-a-hash",
		"$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",  // wrong variant
		"$argon2id$v=18$m=65536,t=3,p=4$c2FsdA$aGFzaA", // wrong version
		"$argon2id$v=19$m=abc$c2FsdA$aGFzaA",           // bad params
		"$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",    // bad b64 salt
		"$argon2id$v=19$m=65536,t=3,p=4$c2FsdA",        // truncated
	}
	for _, b := range bad {
		if _, err := VerifyPassword("anything", b); err != ErrInvalidPasswordHash {
			t.Fatalf("hash %q: want ErrInvalidPasswordHash, got %v", b, err)
		}
	}
}

func TestRehashDetection(t *testing.T) {
	weak, err := hashPasswordWithParams("password-1", ArgonParams{
		Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32,
	})
	if err != nil {
		t.Fatalf("weak hash: %v", err)
	}
	needsRehash, err := VerifyPassword("password-1", weak)
	if err != nil {
		t.Fatalf("verify weak: %v", err)
	}
	if !needsRehash {
		t.Fatal("changed params must trigger rehash")
	}
}

func TestHashPasswordValidation(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password must fail")
	}
	if _, err := HashPassword(strings.Repeat("x", MaxPasswordBytes+1)); err == nil {
		t.Fatal("oversize password must fail")
	}
	if err := ValidatePassword("short"); err == nil {
		t.Fatal("short password must fail")
	}
	if err := ValidatePassword("long-enough-1"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}

func TestValidateUsername(t *testing.T) {
	valid := []string{"gm", "Alice_99", "a.b-c_d", strings.Repeat("x", 64)}
	for _, v := range valid {
		if err := ValidateUsername(v); err != nil {
			t.Fatalf("username %q rejected: %v", v, err)
		}
	}
	invalid := []string{
		"", ".", "..", "a/b", `a\b`, "a b", "a|b", "a@b", "tab\there",
		"ünïcode", strings.Repeat("x", 65), "../vault", "a:b",
	}
	for _, v := range invalid {
		if err := ValidateUsername(v); err == nil {
			t.Fatalf("username %q accepted", v)
		}
	}
}
