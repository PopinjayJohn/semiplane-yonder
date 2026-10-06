// Round-trip fuzz: vault files stay Obsidian-safe (AGENTS.md hard rule —
// parsers never rewrite source) and Parse never panics on hostile input.
// Runs against the REAL markdown.Parse (Lane A, merged at Phase 1).
package audit

import (
	"context"
	"testing"

	"github.com/semiplane/yonder/internal/markdown"
)

// FuzzVaultRoundTrip feeds arbitrary bytes at Parse level. Corpus seeds in
// testdata/fuzz/FuzzVaultRoundTrip cover the secret-model shapes; the
// fuzzer explores the rest during `make fuzz-short`.
func FuzzVaultRoundTrip(f *testing.F) {
	f.Add("---\ntitle: T\nsecret: true\nowner: alice\n---\n# T\nBody text.\n", "secret.md")
	f.Add("> [!secret]- hidden\n> concealed cacheword\n", "blocks.md")
	f.Add("> [!secret]+ aside\n> open asideword\n", "blocks-plus.md")
	f.Add("---\ntitle: [unclosed\nsecret: true\n---\n# T\n", "quarantine.md")
	f.Add("---\ntitle: t\n---\nSee ![[secret]] and [[secret|alias]] #tag\n", "embeds.md")
	f.Add("# Plain\n\nJust a paragraph with \xff\xfe bytes nearby.\n", "binary-ish.md")
	f.Add("", "empty.md")
	f.Add("---\n", "fm-stub.md")

	f.Fuzz(func(t *testing.T, content, pagePath string) {
		if pagePath == "" {
			pagePath = "fuzz.md"
		}
		p, err := markdown.Parse(context.Background(), content, pagePath)
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}
		if p == nil {
			t.Fatal("nil page")
		}
		if p.Content != content {
			t.Fatal("Content rewritten: parser must never modify source")
		}
		if p.Quarantined && !p.Secret {
			t.Fatal("quarantined page not fail-closed secret")
		}
		// Re-parse is stable: same input renders identical HTML.
		p2, err := markdown.Parse(context.Background(), content, pagePath)
		if err != nil {
			t.Fatalf("re-parse error: %v", err)
		}
		if p2.HTML != p.HTML {
			t.Fatal("unstable render across re-parse")
		}
		if p2.Content != content {
			t.Fatal("Content rewritten on re-parse")
		}
	})
}
