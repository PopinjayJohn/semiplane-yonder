package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reindex rebuilds the disposable index DB from vault files. Atomic:
// builds a temp DB in the same directory (same volume), applies index
// migrations, indexes every page, then renames over the live file.
//
// Red lines (P04/pitfalls):
//   - Only indexPath is ever opened. The app DB (<name>.app.db) is never
//     touched — it is not even a parameter.
//   - Pragmas travel via DSN (see dsn()); the single writer holds
//     MaxOpenConns(1).
//   - Startup rescan and manual `reindex` share indexOne via Rescan.
func Reindex(ctx context.Context, vaultRoot, indexPath string, p Parser) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tmp := IndexTempPath(indexPath, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	db, err := openIndex(tmp)
	if err != nil {
		return fmt.Errorf("open temp index db: %w", err)
	}
	// Build fully before any rename; on failure the live DB is untouched.
	buildErr := func() error {
		if err := NewMigrationRunner(db).RunIndex(ctx, db); err != nil {
			return fmt.Errorf("apply index migrations: %w", err)
		}
		if _, err := Rescan(ctx, db, vaultRoot, p); err != nil {
			return err
		}
		// Checkpoint so the rename carries a complete image.
		if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return fmt.Errorf("checkpoint: %w", err)
		}
		return nil
	}()
	_ = db.Close()
	if buildErr != nil {
		_ = os.Remove(tmp)
		_ = os.Remove(tmp + "-wal")
		_ = os.Remove(tmp + "-shm")
		return buildErr
	}
	// os.Rename replaces the destination on all three OSes (Go uses
	// MOVEFILE_REPLACE_EXISTING on Windows); same-dir temp ⇒ atomic.
	if err := os.Rename(tmp, indexPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish index db: %w", err)
	}
	_ = os.Remove(indexPath + "-wal")
	_ = os.Remove(indexPath + "-shm")
	return nil
}

// RescanStats reports what an incremental Rescan did.
type RescanStats struct {
	Scanned int
	Updated int
	Deleted int
}

// Rescan incrementally reconciles an OPEN index DB with the vault: new and
// mtime/hash-changed markdown files are (re)indexed via indexOne, vanished
// files are purged, and *.conflict-*.md files are recorded in conflicts with
// the parent page's ACL (never indexed into FTS). Non-markdown files are
// tracked as assets by extension (images/pdf), everything else is ignored.
//
// The same function backs startup rescan, watcher-triggered refresh, and the
// bulk phase of Reindex — one code path (P04).
func Rescan(ctx context.Context, db *sql.DB, vaultRoot string, p Parser) (RescanStats, error) {
	var st RescanStats
	seen := map[string]bool{}
	err := walkVault(vaultRoot, func(rel string, info fs.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[rel] = true
		lower := strings.ToLower(rel)
		switch {
		case isConflictPath(rel):
			st.Scanned++
			return recordConflict(ctx, db, rel, vaultRoot)
		case strings.HasSuffix(lower, ".md"):
			st.Scanned++
			changed, err := indexOne(ctx, db, vaultRoot, rel, info, p)
			if err != nil {
				return err
			}
			if changed {
				st.Updated++
			}
			return nil
		case isAssetName(lower):
			return indexAsset(ctx, db, vaultRoot, rel, info)
		default:
			return nil
		}
	})
	if err != nil {
		return st, err
	}
	// Purge rows for files that vanished from the vault.
	rows, err := db.QueryContext(ctx, `SELECT path FROM vault_files`)
	if err != nil {
		return st, err
	}
	var tracked []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			return st, err
		}
		tracked = append(tracked, path)
	}
	_ = rows.Close()
	for _, t := range tracked {
		if !seen[t] {
			if err := purgePath(ctx, db, t); err != nil {
				return st, err
			}
			st.Deleted++
		}
	}
	// Drop conflict rows whose files vanished; drop stale asset rows.
	if _, err := db.ExecContext(ctx, `DELETE FROM conflicts WHERE path NOT IN (SELECT path FROM vault_files)`); err != nil {
		return st, err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM assets WHERE path NOT IN (SELECT path FROM vault_files)`); err != nil {
		return st, err
	}
	// Repair pass: conflicts scanned before their parent page (walk order is
	// filesystem order) copy the parent ACL now that all pages are indexed.
	// Orphans keep fail-closed secret + empty owner.
	if _, err := db.ExecContext(ctx, `UPDATE conflicts SET owner = (SELECT owner FROM pages WHERE pages.path = conflicts.parent_path),
		secret = (SELECT secret FROM pages WHERE pages.path = conflicts.parent_path),
		editable_by = (SELECT editable_by FROM pages WHERE pages.path = conflicts.parent_path)
		WHERE EXISTS (SELECT 1 FROM pages WHERE pages.path = conflicts.parent_path)`); err != nil {
		return st, err
	}
	return st, nil
}

// walkVault yields vault-relative posix paths for all regular files under
// root, skipping hidden directories (.obsidian, .git), the data dir (never
// inside the vault, but be safe), and overlong entries. Display case is
// preserved; comparisons happen on demand via path_fold.
func walkVault(root string, fn func(rel string, info fs.FileInfo) error) error {
	return filepath.WalkDir(root, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if fp == root {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") {
			return nil
		}
		rel, err := filepath.Rel(root, fp)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return nil // raced delete; purged on next pass
		}
		return fn(rel, info)
	})
}

func isAssetName(lower string) bool {
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".pdf", ".webp", ".gif"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// indexOne (re)indexes a single markdown file if its mtime/size/hash changed
// since vault_files. Returns changed=true when the index was rewritten.
func indexOne(ctx context.Context, db *sql.DB, vaultRoot, rel string, info fs.FileInfo, p Parser) (bool, error) {
	mtime := info.ModTime().UnixNano()
	size := info.Size()
	var oldHash string
	var oldMtime, oldSize int64
	err := db.QueryRowContext(ctx, `SELECT hash, mtime, size FROM vault_files WHERE path = ?`, rel).Scan(&oldHash, &oldMtime, &oldSize)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil && oldMtime == mtime && oldSize == size {
		// Fast path: same mtime + size. All vault writes go through
		// WriteFile (temp + rename ⇒ fresh mtime), so equality means
		// unchanged; external editors that preserve both are caught by
		// the hash check on the next size/mtime change.
		return false, nil
	}
	content, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return false, purgePath(ctx, db, rel)
		}
		return false, err
	}
	hash := HashBytes(content)
	if hash == oldHash {
		_, err := db.ExecContext(ctx, `UPDATE vault_files SET mtime = ?, size = ? WHERE path = ?`, mtime, size, rel)
		return false, err
	}
	parsed, err := p(ctx, content, rel)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", rel, err)
	}
	if err := writePageTx(ctx, db, rel, string(content), hash, mtime, size, parsed); err != nil {
		return false, err
	}
	return true, nil
}

// writePageTx replaces one page's index rows (page + chunks + FTS + links +
// tags + optionals + vault_files) in a single transaction.
func writePageTx(ctx context.Context, db *sql.DB, rel, content, hash string, mtime, size int64, parsed *ParsedPage) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	secret := 0
	if parsed.Secret {
		secret = 1
	}
	editableBy, _ := json.Marshal(parsed.EditableBy)
	if len(editableBy) == 0 {
		editableBy = []byte("[]")
	}
	fm := parsed.FrontmatterJSON
	if fm == "" {
		fm = "{}"
	}
	now := time.Now().UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO pages(path,path_fold,title,content,frontmatter,secret,owner,editable_by,updated_at,hash)
		VALUES(?,lower(?),?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET path_fold=lower(excluded.path),title=excluded.title,content=excluded.content,
		frontmatter=excluded.frontmatter,secret=excluded.secret,owner=excluded.owner,editable_by=excluded.editable_by,
		updated_at=excluded.updated_at,hash=excluded.hash`,
		rel, rel, parsed.Title, content, fm, secret, parsed.Owner, string(editableBy), now, hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blocks WHERE page_id = ?`, rel); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blocks_fts WHERE page_id = ?`, rel); err != nil {
		return err
	}
	// Ordinal 0: synthetic "title + tags" chunk under the page secret flag.
	chunks := append([]ParsedBlock{{Secret: parsed.Secret, Text: TitleTagsChunk(parsed.Title, parsed.Tags)}}, parsed.Chunks...)
	for i, c := range chunks {
		cs := 0
		if c.Secret {
			cs = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blocks(page_id,ordinal,secret,text) VALUES(?,?,?,?)`, rel, i, cs, c.Text); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blocks_fts(page_id,text,ordinal) VALUES(?,?,?)`, rel, c.Text, i); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM links WHERE source = ?`, rel); err != nil {
		return err
	}
	for _, tgt := range parsed.Links {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO links(source,target) VALUES(?,?)`, rel, tgt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE page_id = ?`, rel); err != nil {
		return err
	}
	for _, t := range parsed.Tags {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tags(page_id,tag,secret) VALUES(?,?,?)`, rel, t, secret); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM optionals WHERE file = ?`, rel); err != nil {
		return err
	}
	for _, o := range parsed.Optionals {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO optionals(id,title,file,line) VALUES(?,?,?,?)`, o.ID, o.Title, rel, o.Line); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vault_files(path,hash,mtime,size) VALUES(?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET hash=excluded.hash,mtime=excluded.mtime,size=excluded.size`,
		rel, hash, mtime, size); err != nil {
		return err
	}
	return tx.Commit()
}

// recordConflict registers a *.conflict-<ts>.md file with its PARENT page's
// ACL (owner/secret/editable_by copied at scan time). Orphans (parent gone)
// default to secret + writer-as-owner: conflicts never widen access
// (pitfalls: owner inheritance). Conflict content is never indexed into FTS.
func recordConflict(ctx context.Context, db *sql.DB, rel, vaultRoot string) error {
	parent := parentOfConflict(rel)
	// The writer's identity is not recoverable from the filesystem at scan
	// time, so creator stays empty here; the WriteFile path (same process)
	// records authorship in edits_log on the app DB instead. Conflicts stay
	// fail-closed secret until the parent ACL is found.
	owner, editableBy := "", "[]"
	secret := 1
	if parent != "" {
		err := db.QueryRowContext(ctx, `SELECT owner, secret, editable_by FROM pages WHERE path = ?`, parent).
			Scan(&owner, &secret, &editableBy)
		if err == sql.ErrNoRows {
			// Parent not indexed (yet or deleted): stay fail-closed secret;
			// owner falls back to the conflict writer below when known.
			owner, secret, editableBy = "", 1, "[]"
		} else if err != nil {
			return err
		}
	}
	info, err := os.Stat(filepath.Join(vaultRoot, filepath.FromSlash(rel)))
	if err != nil {
		return nil // raced delete; purged by the vault_files sweep
	}
	content, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	created := info.ModTime().UnixNano()
	_, err = db.ExecContext(ctx, `INSERT INTO conflicts(path,parent_path,creator,created_at,owner,secret,editable_by)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET parent_path=excluded.parent_path,owner=excluded.owner,
		secret=excluded.secret,editable_by=excluded.editable_by`,
		rel, parent, "", created, owner, secret, editableBy)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO vault_files(path,hash,mtime,size) VALUES(?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET hash=excluded.hash,mtime=excluded.mtime,size=excluded.size`,
		rel, HashBytes(content), info.ModTime().UnixNano(), info.Size())
	return err
}

// parentOfConflict strips the single `.conflict-<ts>` infix to recover the
// parent page path: "notes/a.conflict-123.md" → "notes/a.md".
func parentOfConflict(rel string) string {
	i := strings.LastIndex(rel, ".conflict-")
	if i < 0 || !strings.HasSuffix(rel, ".md") {
		return ""
	}
	return rel[:i] + ".md"
}

// purgePath removes every index row for a vanished file (page + chunks + FTS
// + links both directions + tags + its optionals + vault_files + conflicts).
func purgePath(ctx context.Context, db *sql.DB, rel string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM blocks WHERE page_id = ?`, []any{rel}},
		{`DELETE FROM blocks_fts WHERE page_id = ?`, []any{rel}},
		{`DELETE FROM links WHERE source = ? OR target = ?`, []any{rel, rel}},
		{`DELETE FROM tags WHERE page_id = ?`, []any{rel}},
		{`DELETE FROM optionals WHERE file = ?`, []any{rel}},
		{`DELETE FROM pages WHERE path = ?`, []any{rel}},
		{`DELETE FROM conflicts WHERE path = ? OR parent_path = ?`, []any{rel, rel}},
		{`DELETE FROM assets WHERE path = ?`, []any{rel}},
		{`DELETE FROM vault_files WHERE path = ?`, []any{rel}},
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s.q, s.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func indexAsset(ctx context.Context, db *sql.DB, vaultRoot, rel string, info fs.FileInfo) error {
	// Assets are content-addressed by hash of the file bytes; read fully —
	// uploads are capped at 5/10MB by Lane E1, well within memory.
	content, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(rel)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	mime := assetMIME(rel)
	_, err = db.ExecContext(ctx, `INSERT INTO assets(path,mime_type,size,hash,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET mime_type=excluded.mime_type,size=excluded.size,hash=excluded.hash,updated_at=excluded.updated_at`,
		rel, mime, info.Size(), HashBytes(content), info.ModTime().UnixNano())
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO vault_files(path,hash,mtime,size) VALUES(?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET hash=excluded.hash,mtime=excluded.mtime,size=excluded.size`,
		rel, HashBytes(content), info.ModTime().UnixNano(), info.Size())
	return err
}

func assetMIME(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}
