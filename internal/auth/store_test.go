package auth

import (
	"context"
	"testing"
	"time"
)

func TestSessionStoreRoundTrip(t *testing.T) {
	db := openTestDB(t)
	sessions, _ := mustStores(t, db)
	ctx := context.Background()

	csrf, err := NewCSRFToken()
	if err != nil {
		t.Fatalf("csrf: %v", err)
	}
	id, err := sessions.Create(ctx, &Session{
		UserID: "alice", CreatedAt: 1000, ExpiresAt: 2000, IdleAt: 1500,
		Version: 1, CSRFToken: csrf,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := sessions.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != "alice" || got.CSRFToken != csrf || got.Version != 1 {
		t.Fatalf("mismatch: %+v", got)
	}

	got.IdleAt = 1800
	if err := sessions.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = sessions.Get(ctx, id)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.IdleAt != 1800 {
		t.Fatalf("update lost: %+v", got)
	}

	concrete, ok := sessions.(*SessionStoreSQL)
	if !ok {
		t.Fatal("expected *SessionStoreSQL")
	}
	if err := concrete.TouchSession(ctx, id, 1600, 9999); err != nil {
		t.Fatalf("touch: %v", err)
	}
	lastSeen, err := concrete.LastSeen(ctx, id)
	if err != nil {
		t.Fatalf("last seen: %v", err)
	}
	if lastSeen != 1600 {
		t.Fatalf("want last_seen 1600, got %d", lastSeen)
	}

	if err := sessions.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := sessions.Get(ctx, id); err != ErrSessionNotFound {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
	if err := sessions.Delete(ctx, id); err != ErrSessionNotFound {
		t.Fatalf("double delete: want ErrSessionNotFound, got %v", err)
	}
}

func TestSessionStoreNotFoundShapes(t *testing.T) {
	db := openTestDB(t)
	sessions, _ := mustStores(t, db)
	ctx := context.Background()

	for _, id := range []string{"", "nope", "../escape", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		if _, err := sessions.Get(ctx, id); err != ErrSessionNotFound {
			t.Fatalf("get %q: want ErrSessionNotFound, got %v", id, err)
		}
		if err := sessions.Update(ctx, &Session{ID: id}); err != ErrSessionNotFound {
			t.Fatalf("update %q: want ErrSessionNotFound, got %v", id, err)
		}
		if err := sessions.Delete(ctx, id); err != ErrSessionNotFound {
			t.Fatalf("delete %q: want ErrSessionNotFound, got %v", id, err)
		}
	}
}

func TestSessionStoreRevokedInvisible(t *testing.T) {
	db := openTestDB(t)
	sessions, _ := mustStores(t, db)
	ctx := context.Background()

	id, err := sessions.Create(ctx, &Session{
		UserID: "bob", CreatedAt: 1, ExpiresAt: 9999999999, IdleAt: 9999999999,
		Version: 1, CSRFToken: "csrf",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE auth_sessions SET revoked = 1 WHERE id = ?`, id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := sessions.Get(ctx, id); err != ErrSessionNotFound {
		t.Fatalf("revoked row must read as not found, got %v", err)
	}
}

func TestSessionStoreDeleteByUserAndCleanup(t *testing.T) {
	db := openTestDB(t)
	sessions, _ := mustStores(t, db)
	ctx := context.Background()
	future := time.Now().Add(time.Hour).Unix()

	mk := func(user string, exp, idle int64) string {
		id, err := sessions.Create(ctx, &Session{
			UserID: user, CreatedAt: 1, ExpiresAt: exp, IdleAt: idle,
			Version: 1, CSRFToken: "c",
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return id
	}
	keep := mk("alice", future, future)
	mk("alice", future, future)
	mk("bob", future, future)
	deadAbs := mk("carol", 100, future)
	deadIdle := mk("dave", future, 100)

	if err := sessions.DeleteByUser(ctx, "alice"); err != nil {
		t.Fatalf("delete by user: %v", err)
	}
	if _, err := sessions.Get(ctx, keep); err != ErrSessionNotFound {
		t.Fatalf("alice rows must be gone, got %v", err)
	}
	if err := sessions.CleanupExpired(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, id := range []string{deadAbs, deadIdle} {
		if _, err := sessions.Get(ctx, id); err != ErrSessionNotFound {
			t.Fatalf("expired %s must be cleaned, got %v", id, err)
		}
	}
}

func TestUserStoreCRUD(t *testing.T) {
	db := openTestDB(t)
	_, users := mustStores(t, db)
	ctx := context.Background()

	u, err := CreateUser(ctx, users, "gm-user", "password-123", true, 1000)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.SessionVersion != 1 || !u.IsGM {
		t.Fatalf("bad user: %+v", u)
	}
	if u.PasswordHash == "password-123" || u.PasswordHash == "" {
		t.Fatal("password must be stored as PHC hash")
	}

	if _, err := CreateUser(ctx, users, "gm-user", "password-123", false, 1001); err != ErrUserExists {
		t.Fatalf("dup: want ErrUserExists, got %v", err)
	}
	if _, err := CreateUser(ctx, users, "GM-USER", "password-123", false, 1001); err != ErrUserExists {
		t.Fatalf("case-dup: want ErrUserExists, got %v", err)
	}

	got, err := users.GetByUsername(ctx, "gm-user")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Username != "gm-user" {
		t.Fatalf("mismatch: %+v", got)
	}
	if _, err := users.GetByUsername(ctx, "nobody"); err != ErrUserNotFound {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}

	got.FailedLogins = 2
	if err := users.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = users.GetByUsername(ctx, "gm-user")
	if got.FailedLogins != 2 {
		t.Fatalf("update lost: %+v", got)
	}

	list, err := users.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 user, got %d", len(list))
	}
	if err := users.Delete(ctx, "gm-user"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := users.GetByUsername(ctx, "gm-user"); err != ErrUserNotFound {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}
}
