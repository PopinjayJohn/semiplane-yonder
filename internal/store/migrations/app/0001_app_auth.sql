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

-- Lane B: remaining P04 app-file tables (migrated, never rebuilt).
-- Groups for editable-by / grants resolution.
CREATE TABLE IF NOT EXISTS groups (
    name TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS group_members (
    "group" TEXT NOT NULL,
    "user" TEXT NOT NULL, -- users.name (immutable)
    PRIMARY KEY ("group", "user")
);

-- Append-only edit audit (who/what/when).
CREATE TABLE IF NOT EXISTS edits_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    path TEXT NOT NULL,
    op TEXT NOT NULL, -- create/write/delete/rename/resolve-conflict/reindex
    at INTEGER NOT NULL
);

-- Read index of character sheet frontmatter (characters/<pc>/index.md).
-- App DB rows are authoritative for auth/VTT/app state; the sheet file is truth
-- for sheet data, this row is a read copy (AGENTS.md).
CREATE TABLE IF NOT EXISTS characters (
    name TEXT PRIMARY KEY, -- characters/<pc> slug
    owner TEXT NOT NULL,   -- immutable username, stamped by wizard
    ruleset_id TEXT NOT NULL DEFAULT '',
    sheet_json TEXT NOT NULL DEFAULT '{}', -- read copy of index.md frontmatter
    updated_at INTEGER NOT NULL DEFAULT 0
);

-- Campaign sessions + fantasy-calendar events (schema now, UI later).
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    date TEXT NOT NULL DEFAULT '',
    session_id TEXT, -- linked session for clocks scoping (NULL = campaign-wide)
    summary TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    date TEXT NOT NULL,
    session_id TEXT,
    text TEXT NOT NULL
);

-- Clocks (single campaign per process: no campaign_id; session_id NULL = campaign-wide).
CREATE TABLE IF NOT EXISTS clocks (
    id TEXT PRIMARY KEY,
    session_id TEXT,
    name TEXT NOT NULL,
    segments INTEGER NOT NULL,
    filled INTEGER NOT NULL DEFAULT 0
);

-- Wizard drafts (owner: Lane I1; table created here so the schema is complete).
CREATE TABLE IF NOT EXISTS wizard_drafts (
    token TEXT PRIMARY KEY,
    payload TEXT NOT NULL, -- JSON
    created_at INTEGER NOT NULL,
    expires INTEGER NOT NULL
);

-- Dice log (owner: Lane H2 transport; rows pinned with per-viewer re-auth).
CREATE TABLE IF NOT EXISTS dice_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT,
    actor TEXT NOT NULL,
    envelope_json TEXT NOT NULL, -- {intent, actor, targets, tool, context}
    modifiers_json TEXT NOT NULL DEFAULT '[]', -- pinned full modifier list
    result_json TEXT NOT NULL DEFAULT '{}',
    blind INTEGER NOT NULL DEFAULT 0, -- blind routing: GM-only visibility
    created_at INTEGER NOT NULL
);

-- VTT live state (owner: Lane K). Ephemeral: fail closed (hidden) on data loss.
CREATE TABLE IF NOT EXISTS vtt_maps (
    id TEXT PRIMARY KEY,
    calibration TEXT NOT NULL DEFAULT '{}' -- sidecar calibration JSON
);

CREATE TABLE IF NOT EXISTS vtt_tokens (
    map TEXT NOT NULL,
    token TEXT NOT NULL,
    x REAL NOT NULL,
    y REAL NOT NULL,
    hidden INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (map, token)
);

CREATE TABLE IF NOT EXISTS vtt_fog (
    map TEXT PRIMARY KEY,
    mask TEXT NOT NULL DEFAULT '' -- fog mask; missing/empty = fully hidden
);
