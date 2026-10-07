package web

// Adapter tests for ParsePage (R1 amend): the production markdown.Parse ->
// store.ParsedPage conversion used by `reindex`. Covers the SRD spot check
// (goblin/fighter/spell parse to sane ParsedPages) and the fixtures/p01
// trio (links/embeds/secret-block chunk flags).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/store"
)

func mustParseFile(t *testing.T, rel string) []byte {
	t.Helper()
	// Tests run with CWD = the package dir; fixtures live at the repo root.
	raw, err := os.ReadFile(filepath.Join("..", "..", "fixtures", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return raw
}

func hasLink(links []string, want string) bool {
	for _, l := range links {
		if l == want {
			return true
		}
	}
	return false
}

// findChunk returns the first chunk containing want.
func findChunk(chunks []store.ParsedBlock, want string) (store.ParsedBlock, bool) {
	for _, c := range chunks {
		if strings.Contains(c.Text, want) {
			return c, true
		}
	}
	return store.ParsedBlock{}, false
}

func TestParsePageSRDSpotCheck(t *testing.T) {
	ctx := context.Background()
	// Post-Lane-A-amend: same-indent block sequences (`cssclasses:\n- item`)
	// parse, so SRD frontmatter survives with unknown keys intact (kept for
	// `rules lint`). "Sane" here: title recovered (H1 fallback where needed),
	// frontmatter keys preserved, no secret/owner invented (SRD files carry
	// neither key), body chunks carry real phrases.
	cases := []struct {
		fixture string
		title   string
		phrase  string // distinctive body phrase that must land in a chunk
	}{
		{"srd/compendium/bestiary/fey/goblin-boss-xmm.md", "Goblin Boss", "Goblin bosses are often"},
		{"srd/compendium/classes/fighter-xphb.md", "Fighter", "Second Wind"},
		{"srd/compendium/spells/fireball-xphb.md", "Fireball", "8d6"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			pp, err := ParsePage(ctx, mustParseFile(t, tc.fixture), tc.fixture)
			if err != nil {
				t.Fatalf("ParsePage: %v", err)
			}
			if pp.Title != tc.title {
				t.Errorf("title = %q, want %q", pp.Title, tc.title)
			}
			// No secret/owner keys in SRD frontmatter: nothing invented.
			if pp.Secret || pp.Owner != "" {
				t.Errorf("flags: secret=%v owner=%q, want false/\"\"", pp.Secret, pp.Owner)
			}
			var fm map[string]any
			if err := json.Unmarshal([]byte(pp.FrontmatterJSON), &fm); err != nil {
				t.Fatalf("frontmatter JSON: %v", err)
			}
			for _, k := range []string{"aliases", "cssclasses", "obsidianUIMode", "tags"} {
				if _, ok := fm[k]; !ok {
					t.Errorf("frontmatter missing key %q: %s", k, pp.FrontmatterJSON)
				}
			}
			if len(pp.Chunks) == 0 {
				t.Fatalf("no chunks parsed from %s", tc.fixture)
			}
			found := false
			for _, c := range pp.Chunks {
				if strings.Contains(c.Text, tc.phrase) {
					found = true
				}
				if c.Text == "" {
					t.Errorf("empty chunk text in %s", tc.fixture)
				}
			}
			if !found {
				t.Errorf("phrase %q missing from chunks of %s", tc.phrase, tc.fixture)
			}
		})
	}
}

func TestParsePageP01Trio(t *testing.T) {
	ctx := context.Background()

	welcome, err := ParsePage(ctx, mustParseFile(t, "p01/welcome.md"), "welcome.md")
	if err != nil {
		t.Fatalf("welcome: %v", err)
	}
	if welcome.Title != "Welcome to Ashfall" {
		t.Errorf("welcome title = %q", welcome.Title)
	}
	if welcome.Secret || welcome.Owner != "" {
		t.Errorf("welcome secret=%v owner=%q, want false/\"\"", welcome.Secret, welcome.Owner)
	}
	for _, want := range []string{"cinder-pact", "welcome", "lore"} {
		if !hasLink(welcome.Links, want) {
			t.Errorf("welcome links %v missing %q", welcome.Links, want)
		}
	}
	if c, ok := findChunk(welcome.Chunks, "margin note"); !ok || !c.Secret {
		t.Errorf("welcome `-` block: found=%v secret=%v, want found+secret", ok, c.Secret)
	}
	if c, ok := findChunk(welcome.Chunks, "crows"); !ok || c.Secret {
		t.Errorf("welcome `+` block: found=%v secret=%v, want found+open", ok, c.Secret)
	}

	pact, err := ParsePage(ctx, mustParseFile(t, "p01/cinder-pact.md"), "cinder-pact.md")
	if err != nil {
		t.Fatalf("cinder-pact: %v", err)
	}
	if pact.Title != "The Cinder Pact" {
		t.Errorf("pact title = %q", pact.Title)
	}
	if !pact.Secret || pact.Owner != "mira" {
		t.Errorf("pact secret=%v owner=%q, want true/mira", pact.Secret, pact.Owner)
	}
	if len(pact.EditableBy) != 1 || pact.EditableBy[0] != "bram" {
		t.Errorf("pact editable-by = %v, want [bram]", pact.EditableBy)
	}
	if !hasLink(pact.Links, "welcome") {
		t.Errorf("pact links %v missing embed target welcome", pact.Links)
	}
	if c, ok := findChunk(pact.Chunks, "ember"); !ok || !c.Secret {
		t.Errorf("pact `-` block: found=%v secret=%v, want found+secret", ok, c.Secret)
	}

	lore, err := ParsePage(ctx, mustParseFile(t, "p01/lore.md"), "lore.md")
	if err != nil {
		t.Fatalf("lore: %v", err)
	}
	if lore.Title != "Lands Around Ashfall" {
		t.Errorf("lore title = %q", lore.Title)
	}
	if lore.Secret {
		t.Error("lore unexpectedly secret:true")
	}
	for _, want := range []string{"welcome", "cinder-pact"} {
		if !hasLink(lore.Links, want) {
			t.Errorf("lore links %v missing %q", lore.Links, want)
		}
	}
}

// TestParsePageOptionalAndUnsupported pins the two block-type edges: the
// structural optional marker becomes an optional (with its source line),
// and dataview fences (unsupported) never become chunks.
func TestParsePageOptionalAndUnsupported(t *testing.T) {
	ctx := context.Background()
	content := "---\ntitle: T\n---\n\n# T\n\n<!-- optional:id=psyche -->\n\n```dataview\nTABLE x\n```\n"
	pp, err := ParsePage(ctx, []byte(content), "opt.md")
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if len(pp.Optionals) != 1 {
		t.Fatalf("optionals = %v, want 1", pp.Optionals)
	}
	o := pp.Optionals[0]
	if o.ID != "psyche" || o.Title != "psyche" {
		t.Errorf("optional = %+v, want id/title psyche", o)
	}
	if o.Line <= 0 {
		t.Errorf("optional line = %d, want source line", o.Line)
	}
	for _, c := range pp.Chunks {
		if strings.Contains(c.Text, "dataview") || strings.Contains(c.Text, "TABLE x") {
			t.Errorf("unsupported fence leaked into chunks: %q", c.Text)
		}
	}
}
