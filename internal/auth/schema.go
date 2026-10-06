package auth

import (
	"context"
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// EnsureAuthSchema creates the auth-owned tables in the app DB if missing.
//
// Tables: users, auth_sessions, claim_tokens, login_attempts.
//
// Lane split note: migrations under internal/store/migrations are owned by
// Lane B (contents) / Lane E2 (runner). Until the auth tables land there via
// an explicit amend, this function is the interim DDL source: constructors
// in this package call it idempotently (CREATE TABLE IF NOT EXISTS), and the
// statements below are written so they can be lifted verbatim into a
// migration. The app DB is migrated, never rebuilt (Phase 0c).
func EnsureAuthSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS users(
	name            TEXT PRIMARY KEY,
	password_hash   TEXT NOT NULL,
	is_gm           INTEGER NOT NULL DEFAULT 0,
	created_at      INTEGER NOT NULL DEFAULT 0,
	updated_at      INTEGER NOT NULL DEFAULT 0,
	last_login      INTEGER NOT NULL DEFAULT 0,
	failed_logins   INTEGER NOT NULL DEFAULT 0,
	locked_until    INTEGER NOT NULL DEFAULT 0,
	session_version INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS auth_sessions(
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(name) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	idle_at    INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	csrf_token TEXT NOT NULL DEFAULT '',
	version    INTEGER NOT NULL DEFAULT 1,
	revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id);
CREATE TABLE IF NOT EXISTS claim_tokens(
	token_hash   TEXT PRIMARY KEY,
	created_by   TEXT NOT NULL DEFAULT '',
	new_username TEXT NOT NULL DEFAULT '',
	is_gm        INTEGER NOT NULL DEFAULT 0,
	note         TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	expires_at   INTEGER NOT NULL,
	redeemed_at  INTEGER NOT NULL DEFAULT 0,
	max_uses     INTEGER NOT NULL DEFAULT 1,
	uses         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS login_attempts(
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	ip           TEXT NOT NULL DEFAULT '',
	username     TEXT NOT NULL DEFAULT '',
	attempted_at INTEGER NOT NULL,
	success      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_login_attempts_ip_time ON login_attempts(ip, attempted_at);
CREATE INDEX IF NOT EXISTS idx_login_attempts_user_time ON login_attempts(username, attempted_at);
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := db.ExecContext(ctx, schema)
	return err
}
