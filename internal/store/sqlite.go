package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

// OpenParams controls how a database file is opened. Pragmas travel via the
// DSN (`_pragma=`), never via post-open Exec, so every connection — including
// pool-reopened ones — inherits them (pitfalls: single-binary discipline).
type OpenParams struct {
	// ReadOnly opens without creating the file (used to probe before reindex).
	ReadOnly bool
}

// dsn builds a modernc.org/sqlite DSN with WAL + pragmas in the URL.
func dsn(path string, ro bool) string {
	q := url.Values{}
	q.Set("_pragma", "journal_mode(WAL)")
	q.Set("_pragma", "busy_timeout(5000)")
	q.Set("_pragma", "foreign_keys(1)")
	q.Set("_pragma", "synchronous(NORMAL)")
	if ro {
		return "file:" + path + "?mode=ro&" + q.Encode()
	}
	return "file:" + path + "?" + q.Encode()
}

// openIndex opens (creating) the disposable index DB with a single writer:
// MaxOpenConns(1) serializes all access through one connection, satisfying
// the "one writer to SQLite" rule in v1 (reads share the same connection;
// WAL keeps readers unblocked against the reindex temp DB, never the live one).
func openIndex(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteStore implements Store over two files: the disposable index DB and
// the migrated app DB. Reindex paths only ever open the index file — the app
// DB handle here is for read-path joins owned by later lanes.
type sqliteStore struct {
	indexDB   *sql.DB
	appDB     *sql.DB
	indexPath string
	appPath   string
}

// Open opens (creating) index + app databases at the given file paths.
func Open(indexPath, appPath string) (Store, error) {
	indexDB, err := openIndex(indexPath)
	if err != nil {
		return nil, fmt.Errorf("open index db: %w", err)
	}
	appDB, err := sql.Open("sqlite", dsn(appPath, false))
	if err != nil {
		_ = indexDB.Close()
		return nil, fmt.Errorf("open app db: %w", err)
	}
	appDB.SetMaxOpenConns(1)
	if err := appDB.Ping(); err != nil {
		_ = indexDB.Close()
		_ = appDB.Close()
		return nil, err
	}
	return &sqliteStore{indexDB: indexDB, appDB: appDB, indexPath: indexPath, appPath: appPath}, nil
}

func (s *sqliteStore) IndexDB() *sql.DB { return s.indexDB }
func (s *sqliteStore) AppDB() *sql.DB   { return s.appDB }

func (s *sqliteStore) Close() error {
	err1 := s.indexDB.Close()
	err2 := s.appDB.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// isConflictPath reports the single conflict pattern (*.conflict-<ts>.md).
// Conflict files are never listed or searched; they resolve via conflicts rows.
func isConflictPath(p string) bool {
	base := p
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	i := strings.LastIndex(base, ".conflict-")
	if i < 0 {
		return false
	}
	return strings.HasSuffix(base, ".md")
}

func (s *sqliteStore) PageGet(ctx context.Context, path string) (*Page, error) {
	if isConflictPath(path) {
		return nil, sql.ErrNoRows
	}
	row := s.indexDB.QueryRowContext(ctx, `SELECT path,title,content,frontmatter,secret,owner,editable_by,updated_at,hash
		FROM pages WHERE path_fold = lower(?)`, path)
	return scanPage(row)
}

type rowScanner interface{ Scan(dest ...any) error }

func scanPage(r rowScanner) (*Page, error) {
	var p Page
	var fm sql.NullString
	var secret int
	var editableBy string
	if err := r.Scan(&p.Path, &p.Title, &p.Content, &fm, &secret, &p.Owner, &editableBy, &p.UpdatedAt, &p.Hash); err != nil {
		return nil, err
	}
	p.Secret = secret != 0
	if fm.Valid && fm.String != "" {
		_ = json.Unmarshal([]byte(fm.String), &p.Frontmatter)
	}
	_ = json.Unmarshal([]byte(editableBy), &p.EditableBy)
	return &p, nil
}

func (s *sqliteStore) PageList(ctx context.Context, opts PageListOptions) ([]*Page, error) {
	q := `SELECT path,title,content,frontmatter,secret,owner,editable_by,updated_at,hash FROM pages`
	var args []any
	var where []string
	if opts.Prefix != "" {
		where = append(where, "path LIKE ? ESCAPE '\\'")
		args = append(args, likePrefix(opts.Prefix))
	}
	if !opts.IncludeSecret {
		where = append(where, "secret = 0")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY path"
	if opts.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, opts.Limit)
		if opts.Offset > 0 {
			q += " OFFSET ?"
			args = append(args, opts.Offset)
		}
	}
	rows, err := s.indexDB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		if isConflictPath(p.Path) {
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func likePrefix(p string) string {
	r := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return r.Replace(p) + "%"
}

func (s *sqliteStore) PageUpsert(ctx context.Context, page *Page) error {
	editableBy, _ := json.Marshal(page.EditableBy)
	if editableBy == nil {
		editableBy = []byte("[]")
	}
	fm, _ := json.Marshal(page.Frontmatter)
	secret := 0
	if page.Secret {
		secret = 1
	}
	_, err := s.indexDB.ExecContext(ctx, `INSERT INTO pages(path,path_fold,title,content,frontmatter,secret,owner,editable_by,updated_at,hash)
		VALUES(?,lower(?),?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET path_fold=lower(excluded.path),title=excluded.title,content=excluded.content,
		frontmatter=excluded.frontmatter,secret=excluded.secret,owner=excluded.owner,editable_by=excluded.editable_by,
		updated_at=excluded.updated_at,hash=excluded.hash`,
		page.Path, page.Path, page.Title, page.Content, string(fm), secret, page.Owner, string(editableBy), page.UpdatedAt, page.Hash)
	return err
}

func (s *sqliteStore) PageDelete(ctx context.Context, path string) error {
	tx, err := s.indexDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM blocks WHERE page_id = ?`, []any{path}},
		{`DELETE FROM blocks_fts WHERE page_id = ?`, []any{path}},
		{`DELETE FROM links WHERE source = ? OR target = ?`, []any{path, path}},
		{`DELETE FROM tags WHERE page_id = ?`, []any{path}},
		{`DELETE FROM optionals WHERE file = ?`, []any{path}},
		{`DELETE FROM vault_files WHERE path = ?`, []any{path}},
		{`DELETE FROM pages WHERE path_fold = lower(?)`, []any{path}},
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s.q, s.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Search runs an FTS5 match over chunk text, then filters app-side by viewer
// ACL, then cuts snippets from visible chunks only. Secret titles/paths never
// leave this function for unauthorized viewers: candidates are dropped before
// SearchResult construction, not redacted after.
func (s *sqliteStore) Search(ctx context.Context, query string, opts SearchOptions) ([]*SearchResult, error) {
	q := query
	if q == "" {
		q = opts.Query
	}
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.indexDB.QueryContext(ctx, `SELECT f.page_id, f.text, f.ordinal, bm25(blocks_fts),
		p.title, p.secret, p.owner, p.editable_by
		FROM blocks_fts f JOIN pages p ON p.path = f.page_id
		WHERE blocks_fts MATCH ? ORDER BY bm25(blocks_fts) LIMIT ?`, q, limit*5)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type hit struct {
		pageID, text, title, owner, editableBy string
		ordinal                                int
		rank                                   float64
		secret                                 bool
	}
	byPage := map[string][]hit{}
	order := []string{}
	for rows.Next() {
		var h hit
		var secret int
		if err := rows.Scan(&h.pageID, &h.text, &h.ordinal, &h.rank, &h.title, &secret, &h.owner, &h.editableBy); err != nil {
			return nil, err
		}
		h.secret = secret != 0
		if isConflictPath(h.pageID) {
			continue
		}
		if _, ok := byPage[h.pageID]; !ok {
			order = append(order, h.pageID)
		}
		byPage[h.pageID] = append(byPage[h.pageID], h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []*SearchResult
	for _, pid := range order {
		hs := byPage[pid]
		h0 := hs[0]
		if !PageVisible(h0.secret, pid, h0.owner, h0.editableBy, opts.Viewer) {
			continue // secret title/path hidden, not redacted
		}
		// Snippet from the best-ranked VISIBLE chunk only.
		snippet := ""
		for _, h := range hs {
			chunkSecret := h.secret
			if h.ordinal > 0 {
				chunkSecret = chunkSecretOf(ctx, s, pid, h.ordinal, h.secret)
			}
			if !ChunkVisible(chunkSecret, h0.owner, opts.Viewer) {
				continue
			}
			snippet = cutSnippet(h.text, q)
			break
		}
		if snippet == "" {
			continue // only secret-chunk matches: no visible evidence to show
		}
		out = append(out, &SearchResult{Path: pid, Title: h0.title, Snippet: snippet, Score: h0.rank, Secret: h0.secret})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func chunkSecretOf(ctx context.Context, s *sqliteStore, pageID string, ordinal int, pageSecret bool) bool {
	var secret int
	if err := s.indexDB.QueryRowContext(ctx, `SELECT secret FROM blocks WHERE page_id = ? AND ordinal = ?`, pageID, ordinal).Scan(&secret); err != nil {
		return pageSecret
	}
	return secret != 0
}

// isWordRune reports runes that split query terms for snippet cutting.
func isWordRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r > 127
}

// cutSnippet cuts ±60 runes around the first case-insensitive query term hit.
// Terms come from the raw query; FTS operators are harmless here (plain
// substring scan, no secret content logged).
func cutSnippet(text, query string) string {
	terms := strings.FieldsFunc(query, func(r rune) bool { return !isWordRune(r) })
	lower := strings.ToLower(text)
	idx := -1
	hit := ""
	for _, t := range terms {
		if t == "" {
			continue
		}
		if i := strings.Index(lower, strings.ToLower(t)); i >= 0 {
			idx, hit = i, t
			break
		}
	}
	r := []rune(text)
	if idx < 0 {
		if len(r) > 120 {
			return string(r[:120]) + "…"
		}
		return text
	}
	// byte idx → rune idx
	ri := len([]rune(text[:idx]))
	start := ri - 60
	if start < 0 {
		start = 0
	}
	end := ri + len([]rune(hit)) + 60
	if end > len(r) {
		end = len(r)
	}
	s := string(r[start:end])
	if start > 0 {
		s = "…" + s
	}
	if end < len(r) {
		s += "…"
	}
	return s
}

func (s *sqliteStore) Backlinks(ctx context.Context, path string) ([]string, error) {
	return s.linkDir(ctx, `SELECT source FROM links WHERE target = ? ORDER BY source`, path)
}

func (s *sqliteStore) ForwardLinks(ctx context.Context, path string) ([]string, error) {
	return s.linkDir(ctx, `SELECT target FROM links WHERE source = ? ORDER BY target`, path)
}

func (s *sqliteStore) linkDir(ctx context.Context, q, path string) ([]string, error) {
	rows, err := s.indexDB.QueryContext(ctx, q, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if !isConflictPath(t) {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func (s *sqliteStore) AssetGet(ctx context.Context, path string) (*Asset, error) {
	var a Asset
	err := s.indexDB.QueryRowContext(ctx, `SELECT path,mime_type,size,hash,updated_at FROM assets WHERE path = ?`, path).
		Scan(&a.Path, &a.MIMEType, &a.Size, &a.Hash, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *sqliteStore) AssetList(ctx context.Context, opts AssetListOptions) ([]*Asset, error) {
	q := `SELECT path,mime_type,size,hash,updated_at FROM assets`
	var args []any
	if opts.Prefix != "" {
		q += ` WHERE path LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(opts.Prefix))
	}
	q += ` ORDER BY path`
	if opts.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, opts.Limit)
		if opts.Offset > 0 {
			q += ` OFFSET ?`
			args = append(args, opts.Offset)
		}
	}
	rows, err := s.indexDB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Asset
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.Path, &a.MIMEType, &a.Size, &a.Hash, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (s *sqliteStore) AssetUpsert(ctx context.Context, asset *Asset) error {
	_, err := s.indexDB.ExecContext(ctx, `INSERT INTO assets(path,mime_type,size,hash,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET mime_type=excluded.mime_type,size=excluded.size,hash=excluded.hash,updated_at=excluded.updated_at`,
		asset.Path, asset.MIMEType, asset.Size, asset.Hash, asset.UpdatedAt)
	return err
}

func (s *sqliteStore) AssetDelete(ctx context.Context, path string) error {
	_, err := s.indexDB.ExecContext(ctx, `DELETE FROM assets WHERE path = ?`, path)
	return err
}

// Transact scopes fn to one index-DB transaction; the app DB passes through
// (index transactions never span the precious app file — reindex red-line).
func (s *sqliteStore) Transact(ctx context.Context, fn func(tx Store) error) error {
	tx, err := s.indexDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(&transactor{base: s, tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

type transactor struct {
	base *sqliteStore
	tx   *sql.Tx
}

func (t *transactor) PageGet(ctx context.Context, path string) (*Page, error) {
	return t.base.PageGet(ctx, path)
}
func (t *transactor) PageList(ctx context.Context, opts PageListOptions) ([]*Page, error) {
	return t.base.PageList(ctx, opts)
}
func (t *transactor) PageDelete(ctx context.Context, path string) error {
	return t.base.PageDelete(ctx, path)
}
func (t *transactor) Search(ctx context.Context, query string, opts SearchOptions) ([]*SearchResult, error) {
	return t.base.Search(ctx, query, opts)
}
func (t *transactor) Backlinks(ctx context.Context, path string) ([]string, error) {
	return t.base.Backlinks(ctx, path)
}
func (t *transactor) ForwardLinks(ctx context.Context, path string) ([]string, error) {
	return t.base.ForwardLinks(ctx, path)
}
func (t *transactor) AssetGet(ctx context.Context, path string) (*Asset, error) {
	return t.base.AssetGet(ctx, path)
}
func (t *transactor) AssetList(ctx context.Context, opts AssetListOptions) ([]*Asset, error) {
	return t.base.AssetList(ctx, opts)
}
func (t *transactor) AssetUpsert(ctx context.Context, asset *Asset) error {
	return t.base.AssetUpsert(ctx, asset)
}
func (t *transactor) AssetDelete(ctx context.Context, path string) error {
	return t.base.AssetDelete(ctx, path)
}
func (t *transactor) IndexDB() *sql.DB { return t.base.indexDB }
func (t *transactor) AppDB() *sql.DB   { return t.base.appDB }
func (t *transactor) Close() error     { return nil }
func (t *transactor) Transact(ctx context.Context, fn func(tx Store) error) error {
	return fn(t) // already in transaction; flatten
}
func (t *transactor) PageUpsert(ctx context.Context, page *Page) error {
	editableBy, _ := json.Marshal(page.EditableBy)
	if editableBy == nil {
		editableBy = []byte("[]")
	}
	fm, _ := json.Marshal(page.Frontmatter)
	secret := 0
	if page.Secret {
		secret = 1
	}
	_, err := t.tx.ExecContext(ctx, `INSERT INTO pages(path,path_fold,title,content,frontmatter,secret,owner,editable_by,updated_at,hash)
		VALUES(?,lower(?),?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET path_fold=lower(excluded.path),title=excluded.title,content=excluded.content,
		frontmatter=excluded.frontmatter,secret=excluded.secret,owner=excluded.owner,editable_by=excluded.editable_by,
		updated_at=excluded.updated_at,hash=excluded.hash`,
		page.Path, page.Path, page.Title, page.Content, string(fm), secret, page.Owner, string(editableBy), page.UpdatedAt, page.Hash)
	return err
}

// NewStore creates a new store instance with index and app databases.
// Files: <data-dir>/<name>.index.db (disposable) + <data-dir>/<name>.app.db
// (migrated). dataDir defaults to sibling <vault>-data/ (never inside vault).
func NewStore(dataDir, vaultPath string) (Store, error) {
	indexPath, appPath, _ := DataPaths(dataDir, vaultPath)
	return Open(indexPath, appPath)
}
