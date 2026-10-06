-- 0001 (index target): index database schema (disposable, rebuilt via reindex temp+rename).
-- Lane B owns contents; runner (Lane E2) applies only to <name>.index.db.
-- App tables live in migrations/app/ against <name>.app.db. Each target versions independently from 1.
--
-- Design notes (P04):
--   - Vault FS = truth; every table here is derivable from vault files.
--   - FTS5 is chunk-granular: blocks_fts mirrors blocks(page_id, ordinal).
--     Ordinal 0 is a synthetic chunk holding "title + tags" text, indexed
--     under the page's secret flag; ordinals >= 1 are body chunks in source
--     order, each carrying its own secret flag ([]secret]- default hidden).
--     All other frontmatter (stats, sheet data) is stored in pages.frontmatter
--     JSON only and never enters FTS.
--   - No triggers: writers insert pages/blocks/FTS rows explicitly inside one
--     transaction so chunk flags and FTS stay in lockstep (a trigger on pages
--     cannot see per-chunk flags).
--   - Conflict files (*.conflict-<ts>.md) are recorded in conflicts with the
--     parent page's ACL and are NEVER inserted into pages/blocks/FTS, so they
--     are invisible to search/listing by construction.

CREATE TABLE IF NOT EXISTS vault_files (
    path TEXT PRIMARY KEY, -- vault-relative posix page/asset ID (display case)
    hash TEXT NOT NULL,    -- sha256 hex of file bytes
    mtime INTEGER NOT NULL,-- unix nanos at index time (clash compare)
    size INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pages (
    path TEXT PRIMARY KEY, -- vault-relative posix ID
    path_fold TEXT NOT NULL, -- lower(path): case-insensitive lookup index
    title TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    frontmatter TEXT, -- JSON object (all keys, incl. non-indexed sheet data)
    secret INTEGER NOT NULL DEFAULT 0,
    owner TEXT NOT NULL DEFAULT '',
    editable_by TEXT NOT NULL DEFAULT '[]', -- JSON array of usernames
    updated_at INTEGER NOT NULL,
    hash TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS blocks (
    page_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL, -- 0 = synthetic "title + tags" chunk, >= 1 body chunks
    secret INTEGER NOT NULL DEFAULT 0,
    text TEXT NOT NULL,
    PRIMARY KEY (page_id, ordinal)
);

-- FTS5 mirror of blocks. ordinal is UNINDEXED (never MATCHed, only carried).
CREATE VIRTUAL TABLE IF NOT EXISTS blocks_fts USING fts5(
    page_id, text,
    ordinal UNINDEXED,
    tokenize='porter unicode61'
);

CREATE TABLE IF NOT EXISTS links (
    source TEXT NOT NULL,
    target TEXT NOT NULL,
    PRIMARY KEY (source, target)
);

CREATE TABLE IF NOT EXISTS assets (
    path TEXT PRIMARY KEY, -- vault-relative posix ID
    mime_type TEXT NOT NULL,
    size INTEGER NOT NULL,
    hash TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tags (
    page_id TEXT NOT NULL,
    tag TEXT NOT NULL,
    secret INTEGER NOT NULL DEFAULT 0, -- page secret flag at index time
    PRIMARY KEY (page_id, tag)
);

-- Ruleset optionals offered by base/overlay packs (P05 schema now, UI later).
CREATE TABLE IF NOT EXISTS optionals (
    id TEXT PRIMARY KEY, -- stable id from the pack
    title TEXT NOT NULL,
    file TEXT NOT NULL,  -- defining vault file (posix rel)
    line INTEGER NOT NULL
);

-- Conflict files: indexed with the PARENT page's ACL, hidden from search.
-- The file on disk is <parent>.conflict-<ts>.md (single pattern everywhere);
-- this row copies owner/secret/editable_by from the parent at scan time.
-- Orphan conflicts (parent deleted): owner = writer, secret = 1 (never widen).
CREATE TABLE IF NOT EXISTS conflicts (
    path TEXT PRIMARY KEY, -- conflict file posix rel path
    parent_path TEXT NOT NULL,
    creator TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    secret INTEGER NOT NULL DEFAULT 1,
    editable_by TEXT NOT NULL DEFAULT '[]' -- JSON array
);

CREATE INDEX IF NOT EXISTS idx_pages_fold ON pages(path_fold);
CREATE INDEX IF NOT EXISTS idx_pages_owner ON pages(owner);
CREATE INDEX IF NOT EXISTS idx_pages_secret ON pages(secret);
CREATE INDEX IF NOT EXISTS idx_blocks_page ON blocks(page_id);
CREATE INDEX IF NOT EXISTS idx_links_target ON links(target);
CREATE INDEX IF NOT EXISTS idx_tags_tag ON tags(tag);
CREATE INDEX IF NOT EXISTS idx_conflicts_parent ON conflicts(parent_path);
