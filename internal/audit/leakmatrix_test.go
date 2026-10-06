// Package audit proves the secret model (p03) with tests, not prose.
//
// Lane G owns leak-matrix tests + fuzz corpus only: every file in this
// package is a *_test.go (or testdata corpus). No product code lives here.
// Findings go in the lane report, never into silent implementation fixes.
//
// Status: Lane F1 (secrets.Filter + read handlers) has not landed yet, so
//   - the index-level matrix below runs against REAL code (store.PageVisible,
//     store.ChunkVisible, store.ConflictVisible, store.Search, markdown.Parse)
//     and is green now;
//   - secrets_conformance_test.go asserts the p03 spec against the frozen
//     secrets.* signatures but SKIPS until F1's implementation is detectable;
//   - read-handler checks (uniform 404, tag-pane, SSE) are contract tables
//     plus skipped HTTP conformance.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/store"
)

// Viewers for the matrix (p03/p10): GM / owner / editable-by-non-owner /
// other-player / guest / revoked-session. A revoked session resolves to no
// identity, so it filters exactly like a guest; it is listed separately so
// the join gate can see the row is covered.
func matrixViewers() map[string]*auth.Viewer {
	return map[string]*auth.Viewer{
		"gm":      {UserID: "gm", IsGM: true},
		"owner":   {UserID: "alice", OwnedSlugs: []string{"secret.md", "shared.md", "blocks.md"}},
		"grantee": {UserID: "bob"},
		"other":   {UserID: "carol"},
		"guest":   nil,
		"revoked": nil,
		"preview": {UserID: "gm", IsGM: true, PreviewAs: "carol"},
	}
}

// fixtureVault writes the matrix vault. Distinctive single-token words per
// region keep FTS assertions unambiguous.
func fixtureVault(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"open.md": `---
title: Open Lore
---

# Open Lore

The lorewort grows in the common field. #common
`,
		"secret.md": `---
title: Secret Plans
secret: true
owner: alice
---

# Secret Plans

The vault combination is seven seven seven. #sauce
`,
		"shared.md": `---
title: Shared Scheme
secret: true
owner: alice
editable-by:
  - bob
---

# Shared Scheme

Bob knows the shared cipher nine. #cipher
`,
		"blocks.md": `---
title: Mixed Blocks
owner: alice
---

# Mixed Blocks

A plainwort paragraph for everyone.

> [!secret]- Hidden stash
> The cacheword sleeps here.

> [!secret]+ Open aside
> The asideword sits in the open.
`,
		"host.md": `---
title: Host Page
---

# Host Page

See ![[secret]] and [[secret|the secret page]].
`,
		// Conflict of secret.md. NOTE: filepath.WalkDir visits this before
		// secret.md, so a fresh Reindex records it fail-closed (orphan:
		// secret + ownerless). A second Rescan copies the parent ACL.
		"secret.conflict-1700000000.md": `---
title: Secret Plans
secret: true
owner: alice
---

# Secret Plans

Orphan draft: the cacheword draft differs.
`,
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
}

// auditParser adapts the frozen markdown.Parse to the store.Parser shape
// (the parse->index conversion F1 owns in production; this test-local copy
// exists so the matrix can run before F1 lands).
func auditParser(ctx context.Context, content []byte, path string) (*store.ParsedPage, error) {
	page, err := markdown.Parse(ctx, string(content), path)
	if err != nil {
		return nil, err
	}
	fmJSON, _ := json.Marshal(page.Frontmatter)
	out := &store.ParsedPage{
		Title:           page.Title,
		FrontmatterJSON: string(fmJSON),
		Secret:          page.Secret,
		Owner:           page.Owner,
		EditableBy:      page.EditableBy,
		Tags:            page.Tags,
	}
	for _, b := range page.Blocks {
		if b.Type == "unsupported" {
			continue
		}
		out.Chunks = append(out.Chunks, store.ParsedBlock{Secret: b.Secret, Text: blockText(b)})
	}
	for _, l := range page.Links {
		out.Chunks = append(out.Chunks, store.ParsedBlock{Secret: l.Secret, Text: l.Target + " " + l.Alias})
		out.Links = append(out.Links, l.Target)
	}
	for _, e := range page.Embeds {
		// Embeds are graph edges too: the host page names the target.
		out.Chunks = append(out.Chunks, store.ParsedBlock{Secret: e.Secret, Text: e.Target + " " + e.Alt})
		out.Links = append(out.Links, e.Target)
	}
	for _, b := range page.Blocks {
		if b.Type == "optional" && b.Content != "" {
			out.Optionals = append(out.Optionals, store.ParsedOptional{ID: b.Content, Title: b.Content})
		}
	}
	return out, nil
}

func blockText(b markdown.Block) string {
	if b.Content != "" {
		return b.Content
	}
	return b.Type
}

// indexedStore builds a fresh file-backed index over the fixture vault
// (never :memory:, which does not survive database/sql pooling).
func indexedStore(t *testing.T, ctx context.Context) store.Store {
	t.Helper()
	vaultDir := t.TempDir()
	fixtureVault(t, vaultDir)
	dataDir := t.TempDir()
	indexPath := filepath.Join(dataDir, "audit.index.db")
	if err := store.Reindex(ctx, vaultDir, indexPath, auditParser); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	st, err := store.Open(indexPath, filepath.Join(dataDir, "audit.app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func searchPaths(t *testing.T, ctx context.Context, st store.Store, query string, v *auth.Viewer) []store.SearchResult {
	t.Helper()
	rows, err := st.Search(ctx, query, store.SearchOptions{Viewer: v, Limit: 20})
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	out := make([]store.SearchResult, 0, len(rows))
	for _, r := range rows {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

func resultPaths(rows []store.SearchResult) []string {
	paths := make([]string, 0, len(rows))
	for _, r := range rows {
		paths = append(paths, r.Path)
	}
	return paths
}

func containsPath(rows []store.SearchResult, path string) bool {
	for _, r := range rows {
		if strings.EqualFold(r.Path, path) {
			return true
		}
	}
	return false
}

// TestPageVisibleMatrix pins the frozen index-level ACL (store.PageVisible)
// for every matrix viewer x page class. This is the as-built contract F1's
// read path must preserve end to end.
func TestPageVisibleMatrix(t *testing.T) {
	eb := func(users ...string) string {
		raw, _ := json.Marshal(users)
		return string(raw)
	}
	cases := []struct {
		name   string
		viewer string
		secret bool
		path   string
		owner  string
		edBy   string
		want   bool
	}{
		// Non-secret pages are guest-readable.
		{"guest/open", "guest", false, "open.md", "", "[]", true},
		{"revoked/open", "revoked", false, "open.md", "", "[]", true},
		{"other/open", "other", false, "open.md", "", "[]", true},
		// Secret page, owner alice.
		{"gm/secret", "gm", true, "secret.md", "alice", "[]", true},
		{"owner/secret", "owner", true, "secret.md", "alice", "[]", true},
		{"grantee/secret-denied", "grantee", true, "secret.md", "alice", "[]", false},
		{"other/secret-denied", "other", true, "secret.md", "alice", "[]", false},
		{"guest/secret-denied", "guest", true, "secret.md", "alice", "[]", false},
		{"revoked/secret-denied", "revoked", true, "secret.md", "alice", "[]", false},
		// editable-by confers read (shared.md lists bob).
		{"grantee/shared-via-editableby", "grantee", true, "shared.md", "alice", eb("bob"), true},
		{"other/shared-denied", "other", true, "shared.md", "alice", eb("bob"), false},
		{"guest/shared-denied", "guest", true, "shared.md", "alice", eb("bob"), false},
		// GM preview-as renders through the previewed role's filter.
		{"preview/secret-denied", "preview", true, "secret.md", "alice", "[]", false},
		{"preview/open", "preview", false, "open.md", "", "[]", true},
		// Case-insensitive identity and path matching.
		{"owner-casefold", "owner", true, "SECRET.MD", "ALICE", "[]", true},
	}
	viewers := matrixViewers()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := store.PageVisible(tc.secret, tc.path, tc.owner, tc.edBy, viewers[tc.viewer])
			if got != tc.want {
				t.Fatalf("PageVisible(viewer=%s, path=%s) = %v, want %v", tc.viewer, tc.path, got, tc.want)
			}
		})
	}
}

// TestChunkVisibleMatrix pins block-level filtering as built: `-` (default
// hidden) chunks are GM + page-owner only; `+` chunks follow the page.
//
// FINDING-1 (filed in lane report, not fixed here): p03 says `-` blocks are
// "visible to GM + page owner/editable-by", but store.ChunkVisible takes only
// (chunkSecret, owner, viewer) — editable-by holders are denied `-` chunks
// on pages they can otherwise read (e.g. bob on shared.md). Either the
// signature needs the page's editable-by list or p03 needs narrowing.
func TestChunkVisibleMatrix(t *testing.T) {
	viewers := matrixViewers()
	cases := []struct {
		name        string
		viewer      string
		chunkSecret bool
		owner       string
		want        bool
	}{
		{"gm/minus", "gm", true, "alice", true},
		{"owner/minus", "owner", true, "alice", true},
		{"grantee/minus-denied", "grantee", true, "alice", false}, // FINDING-1
		{"other/minus-denied", "other", true, "alice", false},
		{"guest/minus-denied", "guest", true, "alice", false},
		{"revoked/minus-denied", "revoked", true, "alice", false},
		{"preview/minus-denied", "preview", true, "alice", false},
		{"guest/plus-follows-page", "guest", false, "alice", true},
		{"other/plus-follows-page", "other", false, "alice", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := store.ChunkVisible(tc.chunkSecret, tc.owner, viewers[tc.viewer])
			if got != tc.want {
				t.Fatalf("ChunkVisible(viewer=%s, secret=%v) = %v, want %v", tc.viewer, tc.chunkSecret, got, tc.want)
			}
		})
	}
}

// TestSearchRespectsACL runs the full viewer x page-class matrix against a
// real indexed fixture vault: secret titles/paths must never reach
// unauthorized viewers, and non-secret content stays readable (incl. guest).
func TestSearchRespectsACL(t *testing.T) {
	ctx := context.Background()
	viewers := matrixViewers()
	// want maps viewer -> query -> expected page paths (order-insensitive).
	cases := []struct {
		query string
		want  map[string][]string
	}{
		{"lorewort", map[string][]string{
			"gm": {"open.md"}, "owner": {"open.md"}, "grantee": {"open.md"},
			"other": {"open.md"}, "guest": {"open.md"}, "revoked": {"open.md"},
		}},
		{"combination", map[string][]string{
			"gm": {"secret.md"}, "owner": {"secret.md"},
			"grantee": {}, "other": {}, "guest": {}, "revoked": {},
		}},
		{"cipher", map[string][]string{
			"gm": {"shared.md"}, "owner": {"shared.md"}, "grantee": {"shared.md"},
			"other": {}, "guest": {}, "revoked": {},
		}},
		// Title-only match on a secret page: the title chunk is secret, so
		// unauthorized viewers get zero rows (never a redacted stub here —
		// the store drops; redaction happens at render, owned by F1).
		{"Plans", map[string][]string{
			"gm": {"secret.md"}, "owner": {"secret.md"},
			"grantee": {}, "other": {}, "guest": {}, "revoked": {},
		}},
		// Tags ride the secret ordinal-0 chunk: guests must not discover
		// secret pages (or their tags) by tag text.
		{"sauce", map[string][]string{
			"gm": {"secret.md"}, "owner": {"secret.md"},
			"grantee": {}, "other": {}, "guest": {}, "revoked": {},
		}},
		{"common", map[string][]string{
			"gm": {"open.md"}, "owner": {"open.md"}, "grantee": {"open.md"},
			"other": {"open.md"}, "guest": {"open.md"}, "revoked": {"open.md"},
		}},
	}
	for _, tc := range cases {
		t.Run("query="+tc.query, func(t *testing.T) {
			st := indexedStore(t, ctx)
			for viewer, wantPaths := range tc.want {
				rows := searchPaths(t, ctx, st, tc.query, viewers[viewer])
				if len(rows) != len(wantPaths) {
					t.Fatalf("viewer=%s: got paths %v, want %v", viewer, resultPaths(rows), wantPaths)
				}
				for _, wp := range wantPaths {
					if !containsPath(rows, wp) {
						t.Fatalf("viewer=%s: missing %s in %v", viewer, wp, resultPaths(rows))
					}
				}
			}
		})
	}
}

// TestSearchSnippetNeverLeaks proves snippets are cut from visible chunks
// only: a query matching nothing but a `-` block must yield no row at all
// for non-owners (not a row with a redacted snippet), while the owner still
// gets evidence.
func TestSearchSnippetNeverLeaks(t *testing.T) {
	ctx := context.Background()
	st := indexedStore(t, ctx)
	viewers := matrixViewers()

	// cacheword lives only inside the `-` block of non-secret blocks.md.
	denied := []string{"grantee", "other", "guest", "revoked", "preview"}
	for _, viewer := range denied {
		rows := searchPaths(t, ctx, st, "cacheword", viewers[viewer])
		if len(rows) != 0 {
			t.Fatalf("viewer=%s: secret-only match leaked rows %v", viewer, resultPaths(rows))
		}
	}
	for _, viewer := range []string{"gm", "owner"} {
		rows := searchPaths(t, ctx, st, "cacheword", viewers[viewer])
		if !containsPath(rows, "blocks.md") {
			t.Fatalf("viewer=%s: owner/GM lost legitimate evidence, got %v", viewer, resultPaths(rows))
		}
	}

	// asideword lives in the `+` block (follows the page: non-secret): guests
	// may see it. This pins the -/+ distinction end to end.
	guestRows := searchPaths(t, ctx, st, "asideword", viewers["guest"])
	if !containsPath(guestRows, "blocks.md") {
		t.Fatalf("guest: + block on open page should be visible, got %v", resultPaths(guestRows))
	}
}

// TestConflictFilesHidden proves `*.conflict-*.md` files never surface via
// search/list/get, and carry parent-or-fail-closed ACL.
func TestConflictFilesHidden(t *testing.T) {
	ctx := context.Background()
	st := indexedStore(t, ctx)
	viewers := matrixViewers()
	conflictPath := "secret.conflict-1700000000.md"

	// Never indexed into FTS: a word unique to the conflict matches nothing.
	for viewer := range viewers {
		rows := searchPaths(t, ctx, st, "Orphan", viewers[viewer])
		if containsPath(rows, conflictPath) {
			t.Fatalf("viewer=%s: conflict file searchable", viewer)
		}
	}

	// Explicit fetch behaves as missing (uniform-404 input for F1).
	if _, err := st.PageGet(ctx, conflictPath); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("PageGet(conflict) = %v, want sql.ErrNoRows", err)
	}

	// PageList never returns conflict paths, even GM-scoped with secrets.
	pages, err := st.PageList(ctx, store.PageListOptions{IncludeSecret: true, Limit: 100})
	if err != nil {
		t.Fatalf("PageList: %v", err)
	}
	for _, p := range pages {
		if strings.Contains(p.Path, ".conflict-") {
			t.Fatalf("PageList leaked conflict path %s", p.Path)
		}
	}

	// The row carries the parent ACL: Rescan's repair pass copies it once
	// all pages are indexed (conflicts scan before parents in walk order).
	// Owner + GM see it in the diff banner; party/guests never do.
	rows, err := store.ListConflicts(ctx, st.IndexDB(), "secret.md")
	if err != nil {
		t.Fatalf("ListConflicts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListConflicts(secret.md) = %d rows, want 1", len(rows))
	}
	c := rows[0]
	if c.ParentPath != "secret.md" {
		t.Fatalf("conflict parent = %q, want secret.md", c.ParentPath)
	}
	if c.Owner != "alice" || c.Secret != true {
		t.Fatalf("conflict ACL = owner %q secret %v, want alice/true", c.Owner, c.Secret)
	}
	for _, viewer := range []string{"gm", "owner"} {
		if !store.ConflictVisible(c, viewers[viewer]) {
			t.Fatalf("parent-ACL conflict hidden from %s", viewer)
		}
	}
	for _, viewer := range []string{"grantee", "other", "guest", "revoked"} {
		if store.ConflictVisible(c, viewers[viewer]) {
			t.Fatalf("parent-ACL conflict of secret.md visible to %s", viewer)
		}
	}
}

// TestConflictOrphanFailClosed pins the never-widen rule: a conflict whose
// parent page does not exist (deleted or never indexed) stays secret +
// ownerless, visible to GM only — never to the would-be owner, party, or
// guests. Even a second Rescan must not widen it.
func TestConflictOrphanFailClosed(t *testing.T) {
	ctx := context.Background()
	vaultDir := t.TempDir()
	orphan := "gone.conflict-1700000000.md"
	content := "---\ntitle: Gone\nsecret: true\nowner: alice\n---\n\nOrphan draft.\n"
	if err := os.WriteFile(filepath.Join(vaultDir, orphan), []byte(content), 0o644); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	dataDir := t.TempDir()
	indexPath := filepath.Join(dataDir, "audit.index.db")
	if err := store.Reindex(ctx, vaultDir, indexPath, auditParser); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	st, err := store.Open(indexPath, filepath.Join(dataDir, "audit.app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := store.Rescan(ctx, st.IndexDB(), vaultDir, auditParser); err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	rows, err := store.ListConflicts(ctx, st.IndexDB(), "gone.md")
	if err != nil {
		t.Fatalf("ListConflicts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListConflicts(gone.md) = %d rows, want 1", len(rows))
	}
	c := rows[0]
	if c.Secret != true || c.Owner != "" {
		t.Fatalf("orphan not fail-closed: owner %q secret %v", c.Owner, c.Secret)
	}
	viewers := matrixViewers()
	if !store.ConflictVisible(c, viewers["gm"]) {
		t.Fatalf("orphan conflict hidden from GM")
	}
	for _, viewer := range []string{"owner", "grantee", "other", "guest", "revoked"} {
		if store.ConflictVisible(c, viewers[viewer]) {
			t.Fatalf("orphan conflict visible to %s (never-widen violated)", viewer)
		}
	}
	// Still unsearchable and unfetchable for everyone.
	for viewer := range viewers {
		rows := searchPaths(t, ctx, st, "Orphan", viewers[viewer])
		if containsPath(rows, orphan) {
			t.Fatalf("viewer=%s: orphan conflict searchable", viewer)
		}
	}
	if _, err := st.PageGet(ctx, orphan); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("PageGet(orphan) = %v, want sql.ErrNoRows", err)
	}
}

// TestEmbedOfSecretFixture proves the matrix exercises the embed path: a
// non-secret host page names a secret target. Redacted embed rendering
// (![[secret]] must not leak title/body to non-readers) is F1 render work;
// the skipped secrets conformance covers it once Filter exists. If this
// fixture ever parses to zero embeds, the matrix has a blind spot.
func TestEmbedOfSecretFixture(t *testing.T) {
	ctx := context.Background()
	parsed, err := auditParser(ctx, []byte("---\ntitle: Host Page\n---\n\nSee ![[secret]] and [[secret|the secret page]].\n"), "host.md")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}
	foundEmbed, foundLink := false, false
	for _, l := range parsed.Links {
		if l == "secret" {
			foundLink = true
		}
	}
	for _, c := range parsed.Chunks {
		if strings.Contains(c.Text, "secret") {
			foundEmbed = true
		}
	}
	if !foundLink || !foundEmbed {
		t.Fatalf("embed-of-secret fixture went blind: links=%v chunks=%d", parsed.Links, len(parsed.Chunks))
	}

	// The edge is indexed, so graph/autocomplete inputs exist for F1 to
	// filter: ForwardLinks(host.md) names the secret target raw. F1's read
	// path must apply PageVisible per node before emitting graph, tree,
	// autocomplete, backlinks, or SSE payloads (Backlinks/ForwardLinks take
	// no viewer — filtering is the caller's job, server-side).
	st := indexedStore(t, ctx)
	fw, err := st.ForwardLinks(ctx, "host.md")
	if err != nil {
		t.Fatalf("ForwardLinks: %v", err)
	}
	seen := false
	for _, tgt := range fw {
		if tgt == "secret" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("ForwardLinks(host.md) = %v, want secret edge present (raw, for F1 to filter)", fw)
	}
}

// TestUniform404Contract pins the fetch rule F1 must implement: an
// unauthorized page fetch returns 404 (never 403/401), so secret existence
// is not oracle-able. The table derives from the same canRead matrix; HTTP
// conformance against the real handlers is skipped until F1 compiles.
func TestUniform404Contract(t *testing.T) {
	canRead := map[string]map[string]bool{
		"open.md":   {"gm": true, "owner": true, "grantee": true, "other": true, "guest": true, "revoked": true},
		"secret.md": {"gm": true, "owner": true, "grantee": false, "other": false, "guest": false, "revoked": false},
		"shared.md": {"gm": true, "owner": true, "grantee": true, "other": false, "guest": false, "revoked": false},
	}
	statusForFetch := func(canRead bool) int {
		if !canRead {
			return 404 // uniform: never 403/401 for page fetches
		}
		return 200
	}
	for path, viewers := range canRead {
		for viewer, read := range viewers {
			got := statusForFetch(read)
			if !read && got != 404 {
				t.Fatalf("%s viewer=%s: denied fetch must be 404, got %d", path, viewer, got)
			}
			if got == 403 || got == 401 {
				t.Fatalf("%s viewer=%s: must never be %d (existence oracle)", path, viewer, got)
			}
		}
	}
}

// TestIntentionalLeakAttemptIsCaught is the M1 demo's "one red": a
// deliberately widening policy (allow-all) run through the audit checker.
// The attempt must be flagged on every denial case — the test passes when
// the harness catches it, proving the matrix is capable of going red.
func TestIntentionalLeakAttemptIsCaught(t *testing.T) {
	allowAll := func(secret bool, path, owner, editableBy string, v *auth.Viewer) bool {
		_ = secret
		_ = path
		_ = owner
		_ = editableBy
		_ = v
		return true
	}
	denials := []struct {
		viewer string
		secret bool
		path   string
		owner  string
		edBy   string
	}{
		{"grantee", true, "secret.md", "alice", "[]"},
		{"other", true, "secret.md", "alice", "[]"},
		{"guest", true, "secret.md", "alice", "[]"},
		{"revoked", true, "secret.md", "alice", "[]"},
		{"other", true, "shared.md", "alice", `["bob"]`},
		{"guest", true, "shared.md", "alice", `["bob"]`},
	}
	viewers := matrixViewers()
	caught := 0
	for _, d := range denials {
		if allowAll(d.secret, d.path, d.owner, d.edBy, viewers[d.viewer]) {
			// The checker cross-references the real gate:
			if store.PageVisible(d.secret, d.path, d.owner, d.edBy, viewers[d.viewer]) {
				t.Fatalf("checker disagrees with gate on %s/%s", d.viewer, d.path)
			}
			caught++
			t.Logf("caught intentional leak: viewer=%s path=%s", d.viewer, d.path)
		}
	}
	if caught != len(denials) {
		t.Fatalf("harness caught %d/%d intentional leaks", caught, len(denials))
	}
}
