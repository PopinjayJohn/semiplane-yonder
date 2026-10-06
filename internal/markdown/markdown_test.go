package markdown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// goldenIndex is the JSON-serializable projection of a Page that golden
// files pin. It mirrors the fields the read path and index consume.
type goldenIndex struct {
	Path             string      `json:"path"`
	Title            string      `json:"title"`
	Secret           bool        `json:"secret"`
	Owner            string      `json:"owner,omitempty"`
	EditableBy       []string    `json:"editableBy,omitempty"`
	Tags             []string    `json:"tags,omitempty"`
	Frontmatter      Frontmatter `json:"frontmatter"`
	Links            []Link      `json:"links"`
	Embeds           []Embed     `json:"embeds"`
	TOC              []TOCEntry  `json:"toc"`
	Blocks           []Block     `json:"blocks"`
	Quarantined      bool        `json:"quarantined"`
	QuarantineReason string      `json:"quarantineReason,omitempty"`
	Validation       []string    `json:"validation,omitempty"`
}

func pageIndex(p *Page) goldenIndex {
	links := p.Links
	if links == nil {
		links = []Link{}
	}
	embeds := p.Embeds
	if embeds == nil {
		embeds = []Embed{}
	}
	toc := p.TOC
	if toc == nil {
		toc = []TOCEntry{}
	}
	blocks := p.Blocks
	if blocks == nil {
		blocks = []Block{}
	}
	validation := ValidateFrontmatter(p.Frontmatter)
	sort.Strings(validation)
	return goldenIndex{
		Path:             p.Path,
		Title:            p.Title,
		Secret:           p.Secret,
		Owner:            p.Owner,
		EditableBy:       p.EditableBy,
		Tags:             p.Tags,
		Frontmatter:      p.Frontmatter,
		Links:            links,
		Embeds:           embeds,
		TOC:              toc,
		Blocks:           blocks,
		Quarantined:      p.Quarantined,
		QuarantineReason: p.QuarantineReason,
		Validation:       validation,
	}
}

// casePath returns the vault-relative page ID for a golden case: an
// optional path.txt overrides the default `<case>.md`.
func casePath(dir, kase string) string {
	if raw, err := os.ReadFile(filepath.Join(dir, "path.txt")); err == nil {
		if s := strings.TrimSpace(string(raw)); s != "" {
			return s
		}
	}
	return kase + ".md"
}

func TestGolden(t *testing.T) {
	root := "../../testdata/markdown"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read goldens dir: %v", err)
	}
	update := os.Getenv("UPDATE_GOLDENS") == "1"
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		kase := e.Name()
		dir := filepath.Join(root, kase)
		raw, err := os.ReadFile(filepath.Join(dir, "input.md"))
		if err != nil {
			t.Errorf("%s: no input.md: %v", kase, err)
			continue
		}
		count++
		t.Run(kase, func(t *testing.T) {
			p, err := Parse(context.Background(), string(raw), casePath(dir, kase))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			// Round-trip: source is never rewritten. Content must be
			// byte-identical to the input file.
			if p.Content != string(raw) {
				t.Errorf("Content mutated: got %d bytes, want %d", len(p.Content), len(raw))
			}
			after, err := os.ReadFile(filepath.Join(dir, "input.md"))
			if err != nil || string(after) != string(raw) {
				t.Errorf("input.md changed on disk by Parse")
			}
			// Quarantined pages fail closed (GM-only) and keep raw content.
			if p.Quarantined && !p.Secret {
				t.Errorf("quarantined page must fail closed with Secret=true")
			}

			htmlPath := filepath.Join(dir, "expected.html")
			if update {
				if err := os.WriteFile(htmlPath, []byte(p.HTML), 0o644); err != nil {
					t.Fatalf("write expected.html: %v", err)
				}
			}
			wantHTML, err := os.ReadFile(htmlPath)
			if err != nil {
				t.Fatalf("missing %s (run with UPDATE_GOLDENS=1): %v", htmlPath, err)
			}
			if p.HTML != string(wantHTML) {
				t.Errorf("HTML mismatch:\n--- got ---\n%s\n--- want ---\n%s", p.HTML, wantHTML)
			}

			idx, err := json.MarshalIndent(pageIndex(p), "", "  ")
			if err != nil {
				t.Fatalf("marshal index: %v", err)
			}
			idx = append(idx, '\n')
			jsonPath := filepath.Join(dir, "expected-index.json")
			if update {
				if err := os.WriteFile(jsonPath, idx, 0o644); err != nil {
					t.Fatalf("write expected-index.json: %v", err)
				}
			}
			wantIdx, err := os.ReadFile(jsonPath)
			if err != nil {
				t.Fatalf("missing %s (run with UPDATE_GOLDENS=1): %v", jsonPath, err)
			}
			if string(idx) != string(wantIdx) {
				t.Errorf("index mismatch:\n--- got ---\n%s\n--- want ---\n%s", idx, wantIdx)
			}
		})
	}
	if count == 0 {
		t.Fatal("no golden cases found")
	}
	t.Logf("%d golden cases", count)
}

// TestFrontmatterClosedSet pins validation behavior without goldens.
func TestFrontmatterClosedSet(t *testing.T) {
	fm := Frontmatter{
		"title":    "x",
		"bogus":    1,
		"secret":   "yes",
		"status":   "archived",
		"tags":     "solo",
		"sheet":    map[string]any{"hp": int64(10)},
		"optional": map[string]any{},
	}
	problems := ValidateFrontmatter(fm)
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"bogus", "status", "optional"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected problem mentioning %q, got:\n%s", want, joined)
		}
	}
	for _, notWant := range []string{"title", "secret", "tags", "sheet"} {
		if strings.Contains(joined, `"`+notWant+`"`) {
			t.Errorf("unexpected problem for valid key %q:\n%s", notWant, joined)
		}
	}
}

// TestQuarantineNeverCrashes feeds hostile frontmatter at Parse level.
func TestQuarantineNeverCrashes(t *testing.T) {
	inputs := []string{
		"---\ntitle: [unclosed\n",
		"---\n\tindented: tab\n---\nbody\n",
		"---\n*a: alias\n---\nbody\n",
		"---\nkey: |\n  literal\n   bad-dedent\n---\nbody\n",
		"---\nsecret: [a, b]\n---\nbody\n",
		"---\nno-close: true\n",
		"---\n---\n",
		"",
		"no frontmatter at all\n",
		"--- not frontmatter\nbody\n",
	}
	for i, in := range inputs {
		p, err := Parse(context.Background(), in, "q.md")
		if err != nil {
			t.Fatalf("case %d: Parse returned error: %v", i, err)
		}
		if p.Content != in {
			t.Fatalf("case %d: Content mutated", i)
		}
		if p.Quarantined && !p.Secret {
			t.Fatalf("case %d: quarantined page not fail-closed", i)
		}
	}
}
