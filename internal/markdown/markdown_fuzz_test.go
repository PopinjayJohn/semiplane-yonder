package markdown

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// FuzzParse feeds arbitrary bytes at Parse level: it must never panic,
// never rewrite Content, and quarantined pages must fail closed.
func FuzzParse(f *testing.F) {
	root := "../../testdata/markdown"
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if raw, err := os.ReadFile(filepath.Join(root, e.Name(), "input.md")); err == nil {
			f.Add(string(raw), e.Name()+".md")
		}
	}
	f.Add("---\ntitle: t\nsecret: true\n---\n# Hi [[there]]\n", "seed.md")
	f.Add("> [!secret]- x\n> y\n", "callout.md")
	f.Add("```dataview\nTABLE a\n```\n", "dv.md")

	f.Fuzz(func(t *testing.T, content, pagePath string) {
		if pagePath == "" {
			pagePath = "fuzz.md"
		}
		p, err := Parse(context.Background(), content, pagePath)
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}
		if p == nil {
			t.Fatal("nil page")
		}
		if p.Content != content {
			t.Fatal("Content rewritten")
		}
		if p.Quarantined && !p.Secret {
			t.Fatal("quarantined page not fail-closed")
		}
		// Re-parse is stable: same input renders identical HTML.
		p2, err := Parse(context.Background(), content, pagePath)
		if err != nil {
			t.Fatalf("re-parse error: %v", err)
		}
		if p2.HTML != p.HTML {
			t.Fatal("unstable render")
		}
	})
}
