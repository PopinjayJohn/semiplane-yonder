// Package bench holds the P04 5k-file benchmark harness (Lane E2).
// It runs Lane B's index migrations (never authors schema) against a scratch
// database, then measures cold rebuild (bulk explicit pages/blocks/blocks_fts
// writes, one transaction per chunk), warm single-file rescan (one page
// rewrite), and ACL-filtered player search.
//
// Run it with `make bench` (records internal/bench/results.json) or plain
// `go test ./internal/bench/`. Honor the single-writer rule (pitfalls):
// one connection, WAL, checkpoint on close.
package bench

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/store"
	_ "modernc.org/sqlite"
)

// DefaultNumFiles is the P04 vault size under test.
const DefaultNumFiles = 5000

// DSN returns the locked P01 open string for an index database: WAL,
// 5s busy timeout, FK on, NORMAL sync. Single writer is enforced by the
// caller via SetMaxOpenConns(1).
func DSN(path string) string {
	return path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"
}

// Phase is one timed step of a benchmark run.
type Phase struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
}

// Result is the recorded outcome of a benchmark run.
type Result struct {
	StartedAt string  `json:"started_at"`
	GOOS      string  `json:"goos"`
	GOARCH    string  `json:"goarch"`
	NumCPU    int     `json:"num_cpu"`
	NumFiles  int     `json:"num_files"`
	DBBytes   int64   `json:"db_bytes"`
	Phases    []Phase `json:"phases"`
}

// TotalMs sums all phases.
func (r *Result) TotalMs() int64 {
	var total int64
	for _, p := range r.Phases {
		total += p.DurationMs
	}
	return total
}

// WriteJSON writes the result to path (0644: benchmark numbers are not
// secret content).
func WriteJSON(path string, r *Result) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// Run executes the full harness against a fresh database at dbPath holding
// numFiles synthetic pages. The database file is left on disk for inspection.
func Run(ctx context.Context, dbPath string, numFiles int) (*Result, error) {
	res := &Result{
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
		NumFiles:  numFiles,
	}
	timed := func(name string, fn func() (string, error)) error {
		start := time.Now()
		detail, err := fn()
		res.Phases = append(res.Phases, Phase{
			Name:       name,
			DurationMs: time.Since(start).Milliseconds(),
			Detail:     detail,
		})
		return err
	}

	db, err := sql.Open("sqlite", DSN(dbPath))
	if err != nil {
		return res, err
	}
	db.SetMaxOpenConns(1)
	defer func() {
		_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		_ = db.Close()
	}()

	// migrate: Lane B's index schema, applied — never authored — here.
	if err := timed("migrate", func() (string, error) {
		return "index migrations to empty db", store.NewMigrationRunner(db).RunIndex(ctx, db)
	}); err != nil {
		return res, err
	}

	secrets := map[string]bool{}
	if err := timed("seed-cold", func() (string, error) {
		return fmt.Sprintf("%d pages + links", numFiles), seedPages(ctx, db, numFiles, secrets)
	}); err != nil {
		return res, err
	}

	if err := timed("warm-rescan", func() (string, error) {
		return "rewrite 1 page + reread", warmRescan(ctx, db, numFiles)
	}); err != nil {
		return res, err
	}

	if err := timed("search-guest", func() (string, error) {
		return guestSearch(ctx, db, secrets)
	}); err != nil {
		return res, err
	}

	if err := timed("search-owner", func() (string, error) {
		return ownerSearch(ctx, db)
	}); err != nil {
		return res, err
	}

	if st, err := os.Stat(dbPath); err == nil {
		res.DBBytes = st.Size()
	}
	return res, nil
}

var words = []string{
	"dragon", "tavern", "quest", "sword", "forest", "castle", "potion",
	"ranger", "goblin", "map", "torch", "dungeon", "coin", "spell",
}

// pageBody builds ~1KB of deterministic markdown-ish text. Every 7th page
// mentions dragons so the search phases have a selective term; secret pages
// mention it too, which is exactly what the guest filter must suppress.
func pageBody(i int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Note %d\n\n", i)
	for w := 0; w < 48; w++ {
		fmt.Fprintf(&sb, "The %s of note %d holds a clue. ", words[(i+w)%len(words)], i)
	}
	if i%7 == 0 {
		sb.WriteString("A dragon circles above. ")
	}
	return sb.String()
}

// seedPages bulk-inserts numFiles pages plus a link chain, chunked into
// transactions the way a cold reindex scan would write them. Every 10th page
// is secret (owned by alice); the rest are guest-visible.
func seedPages(ctx context.Context, db *sql.DB, numFiles int, secrets map[string]bool) error {
	const chunk = 500
	for start := 0; start < numFiles; start += chunk {
		end := start + chunk
		if end > numFiles {
			end = numFiles
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if err := insertChunk(ctx, tx, start, end, numFiles, secrets); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func insertChunk(ctx context.Context, tx *sql.Tx, start, end, total int, secrets map[string]bool) error {
	pageStmt, err := tx.PrepareContext(ctx,
		`INSERT INTO pages(path,path_fold,title,content,secret,owner,updated_at,hash) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = pageStmt.Close() }()
	blockStmt, err := tx.PrepareContext(ctx,
		`INSERT INTO blocks(page_id,ordinal,secret,text) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = blockStmt.Close() }()
	ftsStmt, err := tx.PrepareContext(ctx,
		`INSERT INTO blocks_fts(page_id,text,ordinal) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = ftsStmt.Close() }()
	linkStmt, err := tx.PrepareContext(ctx, `INSERT INTO links(source,target) VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = linkStmt.Close() }()

	for i := start; i < end; i++ {
		path := fmt.Sprintf("notes/note-%04d.md", i)
		secret := i%10 == 0
		owner := "gm"
		if secret {
			owner = "alice"
			secrets[path] = true
		}
		title := fmt.Sprintf("Note %d", i)
		body := pageBody(i)
		if _, err := pageStmt.ExecContext(ctx, path, strings.ToLower(path), title, body, boolToInt(secret), owner, i, "hash"); err != nil {
			return err
		}
		// Chunk-granular FTS (Lane B): ordinal 0 holds "title + tags" text,
		// ordinals >= 1 hold body chunks. No triggers; writers insert the
		// blocks and blocks_fts rows explicitly.
		if _, err := blockStmt.ExecContext(ctx, path, 0, boolToInt(secret), title); err != nil {
			return err
		}
		if _, err := ftsStmt.ExecContext(ctx, path, title, 0); err != nil {
			return err
		}
		if _, err := blockStmt.ExecContext(ctx, path, 1, boolToInt(secret), body); err != nil {
			return err
		}
		if _, err := ftsStmt.ExecContext(ctx, path, body, 1); err != nil {
			return err
		}
		if i+1 < total {
			if _, err := linkStmt.ExecContext(ctx, path, fmt.Sprintf("notes/note-%04d.md", i+1)); err != nil {
				return err
			}
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// warmRescan rewrites a single page (the single-file rescan write path) and
// re-reads it through FTS. No triggers: the page row plus its blocks and
// blocks_fts rows are rewritten explicitly, like the real indexer does.
func warmRescan(ctx context.Context, db *sql.DB, numFiles int) error {
	path := fmt.Sprintf("notes/note-%04d.md", numFiles/2)
	if _, err := db.ExecContext(ctx,
		`UPDATE pages SET title = ?, content = ?, updated_at = ? WHERE path = ?`,
		"Note edited", "edited body with dragon", 999999, path); err != nil {
		return fmt.Errorf("warm rewrite: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM blocks WHERE page_id = ?`, path); err != nil {
		return fmt.Errorf("warm rewrite: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM blocks_fts WHERE page_id = ?`, path); err != nil {
		return fmt.Errorf("warm rewrite: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO blocks(page_id,ordinal,secret,text) VALUES(?,?,0,?)`, path, 0, "Note edited"); err != nil {
		return fmt.Errorf("warm rewrite: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO blocks_fts(page_id,text,ordinal) VALUES(?,?,?)`, path, "Note edited", 0); err != nil {
		return fmt.Errorf("warm rewrite: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO blocks(page_id,ordinal,secret,text) VALUES(?,?,0,?)`, path, 1, "edited body with dragon"); err != nil {
		return fmt.Errorf("warm rewrite: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO blocks_fts(page_id,text,ordinal) VALUES(?,?,?)`, path, "edited body with dragon", 1); err != nil {
		return fmt.Errorf("warm rewrite: %w", err)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(DISTINCT page_id) FROM blocks_fts WHERE blocks_fts MATCH 'edited'`).Scan(&n); err != nil {
		return fmt.Errorf("warm reread: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("warm reread found %d rows, want 1", n)
	}
	return nil
}

// matchTitles runs an FTS query over the chunk-granular blocks_fts table and
// returns distinct path/title/secret/owner rows (one row per page even
// though each page contributes two chunks).
func matchTitles(ctx context.Context, db *sql.DB, term string) ([][4]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT p.path, p.title, p.secret, p.owner FROM blocks_fts JOIN pages p ON p.path = blocks_fts.page_id WHERE blocks_fts MATCH ?`,
		term)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out [][4]string
	for rows.Next() {
		var path, title, secret, owner string
		if err := rows.Scan(&path, &title, &secret, &owner); err != nil {
			return nil, err
		}
		out = append(out, [4]string{path, title, secret, owner})
	}
	return out, rows.Err()
}

// filterForViewer is the v1 app-side ACL filter (P04): FTS matches, then
// drop every secret row the viewer may not see. Page-level flags only; the
// seed writes identical flags to each chunk's blocks.secret.
func filterForViewer(rows [][4]string, user string) [][4]string {
	var out [][4]string
	for _, r := range rows {
		secret, owner := r[2] == "1", r[3]
		if !secret || owner == user {
			out = append(out, r)
		}
	}
	return out
}

// guestSearch models the filtered player search (P04): FTS match, then an
// app-side ACL filter. Secret titles must never reach the guest.
func guestSearch(ctx context.Context, db *sql.DB, secrets map[string]bool) (string, error) {
	rows, err := matchTitles(ctx, db, "dragon")
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("guest search matched nothing; fixture broken")
	}
	visible := filterForViewer(rows, "")
	leaked := len(rows) - len(visible)
	for _, r := range visible {
		if r[2] == "1" || secrets[r[0]] {
			return "", fmt.Errorf("guest search leaked secret page %s", r[0])
		}
	}
	if len(visible) == 0 {
		return "", fmt.Errorf("guest search filtered everything; fixture broken")
	}
	return fmt.Sprintf("%d matched, %d visible, %d secret suppressed", len(rows), len(visible), leaked), nil
}

// ownerSearch models alice reading her own secret pages: they must appear.
func ownerSearch(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := matchTitles(ctx, db, "dragon")
	if err != nil {
		return "", err
	}
	visible := filterForViewer(rows, "alice")
	own := 0
	for _, r := range visible {
		if r[2] != "1" {
			continue
		}
		if r[3] != "alice" {
			return "", fmt.Errorf("owner search leaked %s's secret page %s", r[3], r[0])
		}
		own++
	}
	if own == 0 {
		return "", fmt.Errorf("owner search found no secret pages; fixture broken")
	}
	return fmt.Sprintf("%d own secret pages visible", own), nil
}
