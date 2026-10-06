-- 0001 (app target): app database schema (migrated, never rebuilt).
-- File: <name>.app.db. Lane B owns contents; runner (Lane E2) applies via user_version.
-- Tables per P04 (app file) + P11 (Option A sessions).
-- Stub for Phase 0: column detail lands in Lane B / Lane C.

CREATE TABLE IF NOT EXISTS users (
    name TEXT PRIMARY KEY, -- immutable username, no rename in v1
    role TEXT NOT NULL, -- 'gm' or 'player'
    password_hash TEXT NOT NULL, -- argon2id PHC
    session_version INTEGER NOT NULL DEFAULT 1, -- bumped by reset-password
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_sessions (
    id TEXT PRIMARY KEY, -- random 128-bit hex
    user_id TEXT NOT NULL, -- users.name
    expiry INTEGER NOT NULL, -- 30d absolute
    last_seen INTEGER NOT NULL, -- 24h sliding idle
    revoked INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (user_id) REFERENCES users(name)
);

CREATE TABLE IF NOT EXISTS login_attempts (
    ip TEXT PRIMARY KEY,
    fails INTEGER NOT NULL DEFAULT 0,
    locked_until INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS claim_tokens (
    token TEXT PRIMARY KEY,
    slug TEXT NOT NULL,
    expires INTEGER NOT NULL
);
