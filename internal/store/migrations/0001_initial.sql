-- Initial schema for index database
-- This is a placeholder - Lane B owns migration content

CREATE TABLE IF NOT EXISTS pages (
    path TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    content TEXT NOT NULL,
    frontmatter TEXT, -- JSON
    secret INTEGER NOT NULL DEFAULT 0,
    owner TEXT NOT NULL,
    editable_by TEXT, -- JSON array
    updated_at INTEGER NOT NULL,
    hash TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS assets (
    path TEXT PRIMARY KEY,
    mime_type TEXT NOT NULL,
    size INTEGER NOT NULL,
    hash TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS links (
    source TEXT NOT NULL,
    target TEXT NOT NULL,
    PRIMARY KEY (source, target)
);

CREATE INDEX IF NOT EXISTS idx_pages_owner ON pages(owner);
CREATE INDEX IF NOT EXISTS idx_pages_secret ON pages(secret);
CREATE INDEX IF NOT EXISTS idx_links_target ON links(target);

-- FTS5 virtual table for full-text search
CREATE VIRTUAL TABLE IF NOT EXISTS pages_fts USING fts5(
    path, title, content,
    tokenize='porter unicode61'
);

-- Triggers to keep FTS in sync
CREATE TRIGGER IF NOT EXISTS pages_fts_insert AFTER INSERT ON pages BEGIN
    INSERT INTO pages_fts(path, title, content) VALUES (new.path, new.title, new.content);
END;

CREATE TRIGGER IF NOT EXISTS pages_fts_update AFTER UPDATE ON pages BEGIN
    UPDATE pages_fts SET title=new.title, content=new.content WHERE path=new.path;
END;

CREATE TRIGGER IF NOT EXISTS pages_fts_delete AFTER DELETE ON pages BEGIN
    DELETE FROM pages_fts WHERE path=old.path;
END;