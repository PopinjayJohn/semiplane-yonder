-- 0002 (app target): reconcile auth tables to the EnsureAuthSchema shape.
--
-- Context (lanes/G2-amend.md): 0001 created Phase-0 auth tables while
-- internal/auth EnsureAuthSchema (the interim DDL source for SessionStoreSQL,
-- UserStoreSQL, RateLimiter, ClaimStore) creates new-shape ones. runServe runs
-- app migrations first and EnsureAuthSchema second on the same DB; the
-- CREATE TABLE IF NOT EXISTS calls no-op on the old tables and the
-- CREATE INDEX ... (ip, attempted_at) then fails with "no such column:
-- attempted_at", so serve-after-init exits 1. This migration rebuilds the
-- four auth tables to exactly the EnsureAuthSchema shape (column order,
-- types, defaults) so migration + Ensure compose on one DB.
--
-- Shape contract: after 0002, PRAGMA table_info for users, auth_sessions,
-- login_attempts, claim_tokens matches EnsureAuthSchema verbatim, and the
-- idx_auth_sessions_user / idx_login_attempts_ip_time /
-- idx_login_attempts_user_time indexes exist (Ensure re-declares them with
-- IF NOT EXISTS, so both orders compose). Covered by
-- TestApp0002MatchesEnsureAuthSchema in the store package.
--
-- Method: rename + create + copy + drop per table (SQLite has no
-- ALTER COLUMN). The runner applies this file once inside one transaction
-- and bumps user_version to 2 with it; a half-applied 0002 rolls back whole.
-- Non-auth tables (groups, edits_log, characters, sessions, events, clocks,
-- wizard_drafts, dice_logs, vtt_*) are untouched.
--
-- Table-by-table data policy:
--
-- users: 0001 (name, role, password_hash, session_version, created_at) where
-- role is 'gm' or 'player'. role='gm' maps to is_gm=1, anything else to 0.
-- updated_at has no source: backfilled with created_at (the only honest
-- timestamp on the row). last_login / failed_logins / locked_until default 0:
-- the 0001 schema kept no per-account throttle state (its lockout lived in
-- login_attempts, handled below), so there is nothing to carry.
-- session_version and password_hash carry verbatim, so existing GM rows keep
-- working ( verified by the init GM login round-trip test).
--
-- auth_sessions: 0001 (id, user_id, expiry, last_seen, revoked). Live rows
-- are carried so users are not logged out by the upgrade:
--   created_at <- last_seen (no source; a session is necessarily created at
--     or before its last activity, which also keeps ListByUser newest-first
--     ordering sane);
--   expires_at <- expiry (30d absolute deadline preserved verbatim);
--   idle_at <- last_seen + 86400 (24h sliding idle reconstructed from the
--     last-activity marker the old shape maintained);
--   csrf_token <- '' (per-session randomness is ungeneratable in SQLite;
--     ValidateCSRFToken fails closed on empty, so carried sessions stay
--     readable but cannot do state-changing requests until re-login);
--   version <- 1 (no source; matches fresh session_version. If a user's
--     session_version was bumped pre-migration, their carried sessions fail
--     validation afterwards: fail-closed logout, never privilege gain).
-- revoked carries verbatim.
--
-- login_attempts: 0001 (ip PK, fails, locked_until) recorded per-IP
-- counters; the new shape records per-attempt event rows
-- (id, ip, username, attempted_at, success) plus per-account lockout in
-- users.locked_until. Window counts may reset (amend permits it), so plain
-- fail counters are dropped. Active IP lockouts (locked_until in the future)
-- must survive: each such IP is re-seeded with 5 fresh failure rows
-- (= MaxLoginAttempts), which re-arms the 5/5min IP throttle immediately.
-- This replaces the old absolute deadline with a fresh window throttle
-- (documented weakening: at most the window length, not the old absolute
-- remainder). There is no IP->user mapping, so users.locked_until cannot be
-- backfilled from IP rows and stays 0; account lockouts accrue again from
-- post-migration failures.
--
-- claim_tokens: 0001 (token RAW, slug, expires) vs new (token_hash = hex
-- SHA-256 of the raw token, created_by, new_username, is_gm, note,
-- created_at, expires_at, redeemed_at, max_uses, uses). DROP, justified:
-- (1) the hash is not computable inside SQLite, so no faithful migration
-- exists; (2) no product code in the repo ever wrote or read old-shape rows
-- (the only users-table writer is createGMUser; claim flows only exist in
-- the new-shape ClaimStore), so real databases hold at most hand-inserted
-- stub rows; (3) semantics changed anyway: old tokens claimed page slugs,
-- new tokens are GM-issued account invites -- coercing one into the other
-- would mint account invites from page-claim material. Outstanding 0001-era
-- tokens are therefore void after 0002; GMs re-issue.

-- users: role ('gm'/'player') -> is_gm (1/0).
ALTER TABLE users RENAME TO users_0001;
CREATE TABLE users(
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
INSERT INTO users(name, password_hash, is_gm, created_at, updated_at,
	last_login, failed_logins, locked_until, session_version)
SELECT name, password_hash,
	CASE WHEN role = 'gm' THEN 1 ELSE 0 END,
	created_at, created_at,
	0, 0, 0, session_version
FROM users_0001;
DROP TABLE users_0001;

-- auth_sessions: carry live rows with documented defaults (see header).
ALTER TABLE auth_sessions RENAME TO auth_sessions_0001;
CREATE TABLE auth_sessions(
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
INSERT INTO auth_sessions(id, user_id, created_at, expires_at, idle_at,
	last_seen, csrf_token, version, revoked)
SELECT id, user_id, last_seen, expiry, last_seen + 86400,
	last_seen, '', 1, revoked
FROM auth_sessions_0001;
DROP TABLE auth_sessions_0001;
CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id);

-- login_attempts: reset window counters; re-seed active IP lockouts with
-- MaxLoginAttempts (5) fresh failure rows each so the throttle survives.
ALTER TABLE login_attempts RENAME TO login_attempts_0001;
CREATE TABLE login_attempts(
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	ip           TEXT NOT NULL DEFAULT '',
	username     TEXT NOT NULL DEFAULT '',
	attempted_at INTEGER NOT NULL,
	success      INTEGER NOT NULL DEFAULT 0
);
INSERT INTO login_attempts(ip, username, attempted_at, success)
SELECT o.ip, '', CAST(strftime('%s','now') AS INTEGER), 0
FROM login_attempts_0001 AS o
CROSS JOIN (SELECT 1 AS n UNION ALL SELECT 2 UNION ALL SELECT 3
	UNION ALL SELECT 4 UNION ALL SELECT 5)
WHERE o.locked_until > CAST(strftime('%s','now') AS INTEGER);
DROP TABLE login_attempts_0001;
CREATE INDEX IF NOT EXISTS idx_login_attempts_ip_time ON login_attempts(ip, attempted_at);
CREATE INDEX IF NOT EXISTS idx_login_attempts_user_time ON login_attempts(username, attempted_at);

-- claim_tokens: drop-and-recreate (justified in header; unmigratable hash).
DROP TABLE claim_tokens;
CREATE TABLE claim_tokens(
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
