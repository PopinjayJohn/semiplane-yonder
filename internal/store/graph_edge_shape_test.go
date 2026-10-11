package store

import (
	"context"
	"path/filepath"
	"testing"
)

// W1: the links table stores NORMALIZED wikilink targets
// (`NormalizeLinkTarget`: `[[b]]` → `b.md`, `[[B#frag]]` → `B.md`) while
// page IDs carry extensions. The read path (web.resolveGraphTarget) probes
// the normalized form first and keeps the raw extension candidates only as
// fallback for pre-backfill DBs. This test pins the normalized index shape:
// ForwardLinks returns page-path targets, extensioned Backlinks queries hit,
// raw-form queries miss, and no stored target carries a fragment or lacks
// an extension. (Replaces the pre-W1 raw-shape pin TestLinkTargetsStoredRaw;
// the migrations directory is still untouched — no DDL was needed.)
func TestLinkTargetsStoredNormalized(t *testing.T) {
	ctx := context.Background()
	root := testVault(t, map[string]string{
		"a.md": "# Alpha\n\nsee [[b]] and [[B#frag]]\n",
		"b.md": "# Beta\n\nback [[a]]\n",
	})
	idxPath := filepath.Join(t.TempDir(), "edge.index.db")
	if err := Reindex(ctx, root, idxPath, fakeParse); err != nil {
		t.Fatal(err)
	}
	s, err := Open(idxPath, filepath.Join(t.TempDir(), "t.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	fwd, err := s.ForwardLinks(ctx, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	// ORDER BY target (binary): "B.md" sorts before "b.md". Fragments are
	// stripped and the default extension applied at write time.
	if len(fwd) != 2 || fwd[0] != "B.md" || fwd[1] != "b.md" {
		t.Fatalf("ForwardLinks(a.md) = %q, want [B.md b.md] normalized", fwd)
	}
	if fwd, err := s.ForwardLinks(ctx, "b.md"); err != nil || len(fwd) != 1 || fwd[0] != "a.md" {
		t.Fatalf("ForwardLinks(b.md) = %q, %v; want [a.md]", fwd, err)
	}
	// Extensioned queries now HIT the normalized rows: backlinks resolve by
	// page path, which is what visibleBacklinks passes.
	if back, err := s.Backlinks(ctx, "b.md"); err != nil || len(back) != 1 || back[0] != "a.md" {
		t.Fatalf("Backlinks(b.md) = %q, %v; want [a.md]", back, err)
	}
	// Raw-form queries miss normalized rows.
	if back, err := s.Backlinks(ctx, "b"); err != nil || len(back) != 0 {
		t.Fatalf("Backlinks(b) = %q, %v; want empty (normalized shape)", back, err)
	}
	// No stored target carries a fragment or lacks an extension, at the SQL level.
	ss, ok := s.(*sqliteStore)
	if !ok {
		t.Fatalf("store concrete type = %T, want *sqliteStore", s)
	}
	var n int
	if err := ss.indexDB.QueryRow(
		`SELECT COUNT(*) FROM links WHERE target LIKE '%#%' OR target NOT LIKE '%.%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d stored link targets are unnormalized (fragment or extensionless)", n)
	}
}

// W1 backfill: incremental Rescan must converge mixed-shape DBs (raw rows
// left by pre-normalization writes alongside normalized ones) to the
// normalized shape — even for files the mtime fast path skips — and drop
// targets that normalize to nothing. Case is preserved, spelling variants
// collapse, dangling targets keep their (normalized) spelling.
func TestRescanBackfillsRawLinkTargets(t *testing.T) {
	ctx := context.Background()
	root := testVault(t, map[string]string{
		"a.md": "# Alpha\n\nsee [[b]]\n",
		"b.md": "# Beta\n\nback [[a]]\n",
	})
	db, _ := openTempIndex(t, ctx)
	if _, err := Rescan(ctx, db, root, fakeParse); err != nil {
		t.Fatal(err)
	}
	// Simulate the legacy mixed shape: raw targets, a fragment-carrying
	// row, a blank-after-normalize row, and a pre-existing normalized dup
	// of the raw row (collapse must leave exactly one edge).
	for _, q := range []struct {
		query string
		args  []any
	}{
		{`UPDATE links SET target = 'b' WHERE source = 'a.md' AND target = 'b.md'`, nil},
		{`UPDATE links SET target = 'a' WHERE source = 'b.md' AND target = 'a.md'`, nil},
		{`INSERT OR IGNORE INTO links(source,target) VALUES('a.md','B#frag')`, nil},
		{`INSERT OR IGNORE INTO links(source,target) VALUES('a.md','#frag')`, nil},
		{`INSERT OR IGNORE INTO links(source,target) VALUES('a.md','b.md')`, nil},
	} {
		if _, err := db.ExecContext(ctx, q.query, q.args...); err != nil {
			t.Fatalf("seed legacy row: %v", err)
		}
	}
	// No vault file changes: indexOne fast-paths everything, so only the
	// backfill pass can repair the rows.
	if _, err := Rescan(ctx, db, root, fakeParse); err != nil {
		t.Fatal(err)
	}
	st := &sqliteStore{indexDB: db}
	if fwd, err := st.ForwardLinks(ctx, "a.md"); err != nil || len(fwd) != 2 || fwd[0] != "B.md" || fwd[1] != "b.md" {
		t.Fatalf("ForwardLinks(a.md) after backfill = %q, %v; want [B.md b.md]", fwd, err)
	}
	if fwd, err := st.ForwardLinks(ctx, "b.md"); err != nil || len(fwd) != 1 || fwd[0] != "a.md" {
		t.Fatalf("ForwardLinks(b.md) after backfill = %q, %v; want [a.md]", fwd, err)
	}
	if back, err := st.Backlinks(ctx, "b.md"); err != nil || len(back) != 1 || back[0] != "a.md" {
		t.Fatalf("Backlinks(b.md) after backfill = %q, %v; want [a.md]", back, err)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM links WHERE target LIKE '%#%' OR target NOT LIKE '%.%' OR target = ''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d links rows still unnormalized after backfill Rescan", n)
	}
}

// NormalizeLinkTarget unit pins: whitespace/./prefix/fragment handling must
// stay in sync with web.normalizeLinkBase, plus the write-side extension
// default and the empty-target drop.
func TestNormalizeLinkTarget(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"cinder-pact", "cinder-pact.md"},
		{"cinder-pact.md", "cinder-pact.md"},
		{"Cinder-Pact.MD", "Cinder-Pact.MD"},
		{"lore.markdown", "lore.markdown"},
		{"cinder-pact#sigil", "cinder-pact.md"},
		{"B#frag", "B.md"},
		{"./cinder-pact", "cinder-pact.md"},
		{" ./cinder-pact ", "cinder-pact.md"},
		{"././x", "x.md"},
		{"notes/lore", "notes/lore.md"},
		{"pic.png", "pic.png"},
		{"", ""},
		{"   ", ""},
		{"#frag", ""},
		{"./#frag", ""},
	} {
		if got := NormalizeLinkTarget(tc.in); got != tc.want {
			t.Errorf("NormalizeLinkTarget(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
