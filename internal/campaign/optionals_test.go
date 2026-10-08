package campaign

import (
	"strings"
	"testing"
)

func TestScanMultiPerFile(t *testing.T) {
	root := writeVault(t, map[string]string{
		"campaign.yaml": "name: x\n",
		"rules/house.md": `---
title: House Rules
---

## Flanking

Creatures flanking gain advantage.

<!-- optional:id=flanking-2024 -->

## Second Wind

> [!optional|id=second-wind-2024]
> You regain 1d10 + level as a bonus action.
`,
		"notes/flanking.md": `---
title: Flanking Note
optional: true
---

Local variant.
`,
	})
	opts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Optional{}
	for _, o := range opts {
		byID[o.ID] = o
	}
	if len(byID) != 3 {
		t.Fatalf("optionals: %+v", opts)
	}
	if o := byID["flanking-2024"]; o.File != "rules/house.md" || o.Line != 9 || o.Title != "Flanking" {
		t.Fatalf("marker: %+v", o)
	}
	if o := byID["second-wind-2024"]; o.File != "rules/house.md" || o.Line != 13 || o.Title != "Second Wind" {
		t.Fatalf("callout: %+v", o)
	}
	// `optional:true` shorthand: id = path slug.
	if o := byID["notes/flanking"]; o.File != "notes/flanking.md" || o.Line != 1 || o.Title != "Flanking Note" {
		t.Fatalf("shorthand: %+v", o)
	}
}

func TestScanDuplicateFailsWithFileLine(t *testing.T) {
	root := writeVault(t, map[string]string{
		"a.md": "<!-- optional:id=flanking-2024 -->\n",
		"b.md": "> [!optional|id=flanking-2024]\n> text\n",
	})
	_, err := Scan(root)
	if err == nil {
		t.Fatal("want duplicate error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "a.md:1") || !strings.Contains(msg, "b.md:1") ||
		!strings.Contains(msg, "flanking-2024") {
		t.Fatalf("error lacks file:line: %v", err)
	}
}

func TestScanCalloutWithoutIDFails(t *testing.T) {
	root := writeVault(t, map[string]string{
		"a.md": "## Grapple\n\n> [!optional]\n> Shove rules.\n",
	})
	_, err := Scan(root)
	if err == nil {
		t.Fatal("want missing-id error")
	}
	if !strings.Contains(err.Error(), "a.md:3") || !strings.Contains(err.Error(), "explicit id") {
		t.Fatalf("error: %v", err)
	}
}

func TestScanBadShorthandIDFails(t *testing.T) {
	root := writeVault(t, map[string]string{
		"a.md": "---\noptional:\n  id: \"has space\"\n---\n\nBody.\n",
	})
	_, err := Scan(root)
	if err == nil {
		t.Fatal("want bad-id error")
	}
	if !strings.Contains(err.Error(), "a.md:1") {
		t.Fatalf("error: %v", err)
	}
}

func TestScanSkipsConflictsHiddenAndTmp(t *testing.T) {
	root := writeVault(t, map[string]string{
		"notes/a.md":              "<!-- optional:id=kept -->\n",
		"notes/a.conflict-123.md": "<!-- optional:id=dropped-conflict -->\n",
		".obsidian/hide.md":       "<!-- optional:id=dropped-hidden -->\n",
		".dotfile.md":             "<!-- optional:id=dropped-dot -->\n",
		"notes/draft.tmp":         "<!-- optional:id=dropped-tmp -->\n",
		"notes/notmarkdown.txt":   "<!-- optional:id=dropped-txt -->\n",
	})
	opts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 || opts[0].ID != "kept" {
		t.Fatalf("optionals: %+v", opts)
	}
}

func TestScanQuarantinedSkipsShorthand(t *testing.T) {
	root := writeVault(t, map[string]string{
		// Unterminated frontmatter quarantines: shorthand untrusted...
		"a.md": "---\noptional: true\n\nBody.\n\n<!-- optional:id=body-kept -->\n",
	})
	opts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 || opts[0].ID != "body-kept" {
		t.Fatalf("optionals: %+v", opts)
	}
}

func TestScanExplicitShorthandID(t *testing.T) {
	root := writeVault(t, map[string]string{
		"a.md": "---\ntitle: Grapple V2\noptional:\n  id: grapple-v2\n---\n\nBody.\n",
	})
	opts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 || opts[0].ID != "grapple-v2" || opts[0].Title != "Grapple V2" {
		t.Fatalf("optionals: %+v", opts)
	}
}

func TestScanTitleFallbackToID(t *testing.T) {
	root := writeVault(t, map[string]string{
		"a.md": "No headings here.\n\n<!-- optional:id=lonely -->\n",
	})
	opts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 || opts[0].Title != "lonely" {
		t.Fatalf("optionals: %+v", opts)
	}
}

func TestSlugForPath(t *testing.T) {
	for path, want := range map[string]string{
		"notes/Flanking.md":  "notes/flanking",
		"rules/x.markdown":   "rules/x",
		"UPPER/Deep/File.MD": "upper/deep/file",
	} {
		if got := SlugForPath(path); got != want {
			t.Fatalf("SlugForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestScanDuplicateDeterministic(t *testing.T) {
	// Same vault scanned twice reports the same error text (map order
	// must not leak into validation output).
	mk := func() string {
		root := writeVault(t, map[string]string{
			"a.md": "<!-- optional:id=dup -->\n",
			"b.md": "<!-- optional:id=dup -->\n",
		})
		_, err := Scan(root)
		if err == nil {
			t.Fatal("want duplicate error")
		}
		return err.Error()
	}
	a, b := mk(), mk()
	if a != b {
		t.Fatal("duplicate report not deterministic")
	}
}
