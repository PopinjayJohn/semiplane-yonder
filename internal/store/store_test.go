package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
)

// fakeParse is the Lane B test double for markdown.Parse (Phase 1: consume via
// fakes, never import Lane A's package). Convention:
//   - optional --- frontmatter fence with `key: value` lines
//     (secret: true|false, owner: name, editable-by: [a, b], tags: [x, y])
//   - title = first "# " heading, else filename stem
//   - body chunks split on blank lines; chunks starting with "> [!secret]"
//     are secret (leading marker stripped)
//   - [[target]] wikilinks collected in source order
func fakeParse(_ context.Context, content []byte, path string) (*ParsedPage, error) {
	text := string(content)
	pp := &ParsedPage{EditableBy: []string{}}
	rest := text
	if strings.HasPrefix(rest, "---\n") {
		if end := strings.Index(rest[4:], "\n---"); end >= 0 {
			fmBlock := rest[4 : 4+end]
			rest = rest[4+end+4:]
			pp.FrontmatterJSON = "{"
			first := true
			for _, line := range strings.Split(fmBlock, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				k = strings.TrimSpace(k)
				v = strings.TrimSpace(v)
				switch k {
				case "secret":
					pp.Secret = v == "true"
				case "owner":
					pp.Owner = v
				case "editable-by", "tags":
					v = strings.Trim(v, "[]")
					var items []string
					for _, it := range strings.Split(v, ",") {
						it = strings.TrimSpace(it)
						if it != "" {
							items = append(items, it)
						}
					}
					if k == "tags" {
						pp.Tags = items
					} else {
						pp.EditableBy = items
					}
				}
				if !first {
					pp.FrontmatterJSON += ","
				}
				first = false
				pp.FrontmatterJSON += `"` + k + `":"` + v + `"`
			}
			pp.FrontmatterJSON += "}"
		}
	}
	if pp.FrontmatterJSON == "" {
		pp.FrontmatterJSON = "{}"
	}
	var chunks []string
	for _, blk := range strings.Split(rest, "\n\n") {
		blk = strings.TrimSpace(blk)
		if blk == "" {
			continue
		}
		if pp.Title == "" {
			for _, ln := range strings.Split(blk, "\n") {
				if t, ok := strings.CutPrefix(strings.TrimSpace(ln), "# "); ok {
					pp.Title = t
					break
				}
			}
		}
		secret := false
		if s, ok := strings.CutPrefix(blk, "> [!secret]"); ok {
			secret = true
			blk = strings.TrimSpace(s)
		}
		chunks = append(chunks, blk)
		pp.Chunks = append(pp.Chunks, ParsedBlock{Secret: secret, Text: blk})
		_ = chunks
	}
	if pp.Title == "" {
		base := filepath.Base(path)
		pp.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	// links in source order
	rest2 := rest
	for {
		i := strings.Index(rest2, "[[")
		if i < 0 {
			break
		}
		rest2 = rest2[i+2:]
		j := strings.Index(rest2, "]]")
		if j < 0 {
			break
		}
		tgt := rest2[:j]
		if k := strings.Index(tgt, "|"); k >= 0 {
			tgt = tgt[:k]
		}
		pp.Links = append(pp.Links, strings.TrimSpace(tgt))
		rest2 = rest2[j+2:]
	}
	return pp, nil
}

func testVault(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "vault")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func openTempIndex(t *testing.T, ctx context.Context) (*sql.DB, string) {
	t.Helper()
	idxPath := filepath.Join(t.TempDir(), "t.index.db")
	db, err := openIndex(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := NewMigrationRunner(db).RunIndex(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db, idxPath
}

func guest() *auth.Viewer           { return nil }
func gm() *auth.Viewer              { return &auth.Viewer{UserID: "gm", IsGM: true} }
func user(name string) *auth.Viewer { return &auth.Viewer{UserID: name} }
func searchPaths(t *testing.T, ctx context.Context, s Store, q string, v *auth.Viewer) []string {
	t.Helper()
	res, err := s.Search(ctx, q, SearchOptions{Viewer: v, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range res {
		out = append(out, r.Path)
	}
	return out
}

func TestMigrationsApplyClean(t *testing.T) {
	ctx := context.Background()
	db, _ := openTempIndex(t, ctx)
	for _, tbl := range []string{"vault_files", "pages", "blocks", "blocks_fts", "links", "tags", "optionals", "conflicts"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, tbl).Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
	}
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("user_version = %d, %v; want 1", v, err)
	}
}

func TestAppMigrationsApplyClean(t *testing.T) {
	ctx := context.Background()
	appPath := filepath.Join(t.TempDir(), "t.app.db")
	appDB, err := sql.Open("sqlite", dsn(appPath, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appDB.Close() }()
	if err := NewMigrationRunner(appDB).RunApp(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"users", "auth_sessions", "login_attempts", "claim_tokens",
		"groups", "group_members", "edits_log", "characters", "sessions", "events",
		"clocks", "wizard_drafts", "dice_logs", "vtt_maps", "vtt_tokens", "vtt_fog"} {
		var name string
		if err := appDB.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, tbl).Scan(&name); err != nil {
			t.Fatalf("app table %s missing: %v", tbl, err)
		}
	}
}

func TestReindexSearchACL(t *testing.T) {
	ctx := context.Background()
	root := testVault(t, map[string]string{
		"public.md": "# Hello\n\nvisible world text\n",
		"secret.md": "---\nsecret: true\nowner: alice\n---\n# Plans\n\ntakeover plot details\n",
	})
	_, idxPath := openTempIndex(t, ctx)
	// Reindex publishes over idxPath (fresh build path, not the open handle).
	if err := Reindex(ctx, root, idxPath, fakeParse); err != nil {
		t.Fatal(err)
	}
	s, err := Open(idxPath, filepath.Join(t.TempDir(), "t.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if got := searchPaths(t, ctx, s, "world", guest()); len(got) != 1 || got[0] != "public.md" {
		t.Fatalf("guest world search = %v", got)
	}
	if got := searchPaths(t, ctx, s, "takeover", guest()); len(got) != 0 {
		t.Fatalf("guest must not see secret title/snippet: %v", got)
	}
	if got := searchPaths(t, ctx, s, "takeover", user("bob")); len(got) != 0 {
		t.Fatalf("other player must not see secret: %v", got)
	}
	if got := searchPaths(t, ctx, s, "takeover", user("alice")); len(got) != 1 {
		t.Fatalf("owner must see own secret: %v", got)
	}
	if got := searchPaths(t, ctx, s, "takeover", gm()); len(got) != 1 {
		t.Fatalf("GM must see secret: %v", got)
	}
}

func TestReindexRestoresAfterDelete(t *testing.T) {
	ctx := context.Background()
	root := testVault(t, map[string]string{
		"a.md": "# Alpha\n\nfirst light\n",
		"b.md": "# Beta\n\nsee [[a]]\n",
	})
	idxPath := filepath.Join(t.TempDir(), "demo.index.db")
	if err := Reindex(ctx, root, idxPath, fakeParse); err != nil {
		t.Fatal(err)
	}
	// Delete the disposable index DB → reindex restores search identically.
	for _, suf := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(idxPath + suf)
	}
	if err := Reindex(ctx, root, idxPath, fakeParse); err != nil {
		t.Fatal(err)
	}
	s, err := Open(idxPath, filepath.Join(t.TempDir(), "t.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := searchPaths(t, ctx, s, "light", guest()); len(got) != 1 || got[0] != "a.md" {
		t.Fatalf("search after rebuild = %v", got)
	}
	back, err := s.Backlinks(ctx, "a")
	if err != nil || len(back) != 1 || back[0] != "b.md" {
		t.Fatalf("backlinks after rebuild = %v, %v", back, err)
	}
}

func TestReindexNeverTouchesApp(t *testing.T) {
	ctx := context.Background()
	root := testVault(t, map[string]string{"a.md": "# A\n\ntext\n"})
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "x.index.db")
	appPath := filepath.Join(dir, "x.app.db")
	appDB, err := sql.Open("sqlite", dsn(appPath, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appDB.Exec(`CREATE TABLE sentinel(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := appDB.Exec(`INSERT INTO sentinel(id, v) VALUES(1, 'precious')`); err != nil {
		t.Fatal(err)
	}
	_ = appDB.Close()
	if err := Reindex(ctx, root, idxPath, fakeParse); err != nil {
		t.Fatal(err)
	}
	appDB2, err := sql.Open("sqlite", dsn(appPath, true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appDB2.Close() }()
	var v string
	if err := appDB2.QueryRow(`SELECT v FROM sentinel WHERE id = 1`).Scan(&v); err != nil || v != "precious" {
		t.Fatalf("app db touched by reindex: %q, %v", v, err)
	}
}

func TestRescanIncremental(t *testing.T) {
	ctx := context.Background()
	root := testVault(t, map[string]string{"a.md": "# A\n\none\n"})
	db, _ := openTempIndex(t, ctx)
	st, err := Rescan(ctx, db, root, fakeParse)
	if err != nil {
		t.Fatal(err)
	}
	if st.Updated != 1 {
		t.Fatalf("first rescan updated = %d, want 1", st.Updated)
	}
	st, err = Rescan(ctx, db, root, fakeParse)
	if err != nil {
		t.Fatal(err)
	}
	if st.Updated != 0 || st.Deleted != 0 {
		t.Fatalf("warm rescan should be quiet: %+v", st)
	}
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("# A\n\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = Rescan(ctx, db, root, fakeParse)
	if err != nil {
		t.Fatal(err)
	}
	if st.Updated != 1 {
		t.Fatalf("dirty rescan updated = %d, want 1", st.Updated)
	}
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	st, err = Rescan(ctx, db, root, fakeParse)
	if err != nil {
		t.Fatal(err)
	}
	if st.Deleted != 1 {
		t.Fatalf("delete rescan deleted = %d, want 1", st.Deleted)
	}
}

func TestConflictParentACLHiddenFromSearch(t *testing.T) {
	ctx := context.Background()
	root := testVault(t, map[string]string{
		"plans.md": "---\nsecret: true\nowner: alice\n---\n# Plans\n\nsneaky text\n",
	})
	db, _ := openTempIndex(t, ctx)
	if _, err := Rescan(ctx, db, root, fakeParse); err != nil {
		t.Fatal(err)
	}
	// A conflicting save lands on disk in the single pattern.
	cpath := filepath.Join(root, "plans.conflict-1700000000000000000.md")
	if err := os.WriteFile(cpath, []byte("forked sneaky text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Rescan(ctx, db, root, fakeParse); err != nil {
		t.Fatal(err)
	}
	rows, err := ListConflicts(ctx, db, "plans.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("conflicts = %d, want 1", len(rows))
	}
	c := rows[0]
	if c.Owner != "alice" || !c.Secret {
		t.Fatalf("conflict ACL = owner %q secret %v; want alice/true", c.Owner, c.Secret)
	}
	if !ConflictVisible(c, user("alice")) || !ConflictVisible(c, gm()) {
		t.Fatal("owner/GM must see the conflict row")
	}
	if ConflictVisible(c, user("bob")) || ConflictVisible(c, guest()) {
		t.Fatal("other player/guest must not see the conflict row")
	}
	// Hidden from search even for the owner: content lives only in conflicts.
	s, err := Open(filepath.Join(t.TempDir(), "unused.index.db"), filepath.Join(t.TempDir(), "u.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_ = s
	// Search via the rescanned db handle through a store wrapper.
	st := &sqliteStore{indexDB: db}
	if res, err := st.Search(ctx, "forked", SearchOptions{Viewer: user("alice"), Limit: 10}); err != nil || len(res) != 0 {
		t.Fatalf("conflict content must be hidden from search: %v, %v", res, err)
	}
}

func TestChunkLevelSecretSnippet(t *testing.T) {
	ctx := context.Background()
	root := testVault(t, map[string]string{
		"mixed.md": "# Mixed\n\nhello everyone\n\n> [!secret] midnight password\n",
	})
	_, idxPath := openTempIndex(t, ctx)
	if err := Reindex(ctx, root, idxPath, fakeParse); err != nil {
		t.Fatal(err)
	}
	s, err := Open(idxPath, filepath.Join(t.TempDir(), "t.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	// Guest searches the secret-only term: page is public but the only match
	// is a secret chunk → no visible evidence → no hit.
	if res, err := s.Search(ctx, "midnight", SearchOptions{Limit: 10}); err != nil || len(res) != 0 {
		t.Fatalf("guest secret-chunk search = %v, %v", res, err)
	}
	// Public term → snippet comes from the visible chunk, never the secret one.
	res, err := s.Search(ctx, "hello", SearchOptions{Limit: 10})
	if err != nil || len(res) != 1 {
		t.Fatalf("guest public search = %v, %v", res, err)
	}
	if strings.Contains(res[0].Snippet, "midnight") {
		t.Fatalf("snippet leaked secret chunk: %q", res[0].Snippet)
	}
}

func TestDataPaths(t *testing.T) {
	idx, app, key := DataPaths("/data", "/vaults/My Campaign")
	if idx != "/data/my_campaign.index.db" || app != "/data/my_campaign.app.db" || key != "/data/.sessionkey" {
		t.Fatalf("paths = %q %q %q", idx, app, key)
	}
	idx, _, _ = DataPaths("", `/C:\Games\CON`)
	if !strings.HasSuffix(idx, ".index.db") || strings.Contains(strings.ToUpper(idx), "/CON.") {
		t.Fatalf("reserved name not escaped: %q", idx)
	}
	name := dbName("C:\\Games\\Aux Tale")
	if name != "aux_tale" {
		t.Fatalf("dbName = %q", name)
	}
	// Windows-reserved stems are escaped, never emitted raw.
	for _, w := range []string{"CON", "prn", "aux", "nul", "com1", "lpt9"} {
		if dbName("/v/"+w) == w {
			t.Fatalf("reserved stem %q not escaped", w)
		}
	}
}
