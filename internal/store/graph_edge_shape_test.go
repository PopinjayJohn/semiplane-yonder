package store

import (
	"context"
	"path/filepath"
	"testing"
)

// F2-filed graph edge-key follow-up: the links table stores RAW wikilink
// targets (`b`, never `b.md`) while page IDs carry extensions. The read
// path compensates handler-side (web.resolveGraphTarget). This test pins
// the frozen index shape that compensation relies on: ForwardLinks returns
// raw targets verbatim (alias-split, fragment kept), extensioned Backlinks
// queries miss (callers must query the raw form), and no stored target
// carries a page extension. If Lane B ever normalizes link targets, this
// test fails loudly and the handler workaround must be revisited — see the
// Lane B amend proposal in the delivery report (no DDL was improvised
// here; the migrations directory is untouched).
func TestLinkTargetsStoredRaw(t *testing.T) {
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
	// ORDER BY target (binary): "B#frag" sorts before "b".
	if len(fwd) != 2 || fwd[0] != "B#frag" || fwd[1] != "b" {
		t.Fatalf("ForwardLinks(a.md) = %q, want [B#frag b] raw", fwd)
	}
	if fwd, err := s.ForwardLinks(ctx, "b.md"); err != nil || len(fwd) != 1 || fwd[0] != "a" {
		t.Fatalf("ForwardLinks(b.md) = %q, %v; want [a] raw", fwd, err)
	}
	// Extensioned queries miss raw rows: Backlinks callers must use the
	// raw target form (or resolve handler-side like the graph does).
	if back, err := s.Backlinks(ctx, "b.md"); err != nil || len(back) != 0 {
		t.Fatalf("Backlinks(b.md) = %q, %v; want empty (raw shape)", back, err)
	}
	if back, err := s.Backlinks(ctx, "b"); err != nil || len(back) != 1 || back[0] != "a.md" {
		t.Fatalf("Backlinks(b) = %q, %v; want [a.md]", back, err)
	}
	// No stored target carries a page extension, at the SQL level.
	ss, ok := s.(*sqliteStore)
	if !ok {
		t.Fatalf("store concrete type = %T, want *sqliteStore", s)
	}
	var n int
	if err := ss.indexDB.QueryRow(
		`SELECT COUNT(*) FROM links WHERE target LIKE '%.md' OR target LIKE '%.markdown'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d stored link targets carry page extensions; raw shape broken", n)
	}
}
