package plugins

import (
	"strings"
	"testing"
)

func treeInput(entries ...VaultTreeEntry) VaultTreeInput {
	return NewVaultTreeInput(entries)
}

func TestBuildTreeHierarchy(t *testing.T) {
	in := treeInput(
		VaultTreeEntry{Path: "quests/ambush", Title: "Ambush"},
		VaultTreeEntry{Path: "quests/deep/heist", Title: "Heist"},
		VaultTreeEntry{Path: "welcome", Title: "Welcome"},
	)
	root, err := BuildTree(in, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Pages) != 1 || root.Pages[0].Path != "welcome" {
		t.Fatalf("root pages = %+v, want [welcome]", root.Pages)
	}
	if len(root.Folders) != 1 || root.Folders[0].Name != "quests" {
		t.Fatalf("root folders = %+v, want [quests]", root.Folders)
	}
	q := root.Folders[0]
	if len(q.Pages) != 1 || q.Pages[0].Path != "quests/ambush" {
		t.Fatalf("quests pages = %+v", q.Pages)
	}
	if len(q.Folders) != 1 || q.Folders[0].Path != "quests/deep" {
		t.Fatalf("quests folders = %+v", q.Folders)
	}
	if len(q.Folders[0].Pages) != 1 || q.Folders[0].Pages[0].Path != "quests/deep/heist" {
		t.Fatalf("quests/deep pages = %+v", q.Folders[0].Pages)
	}
}

func TestBuildTreeDeterministicOrder(t *testing.T) {
	entries := []VaultTreeEntry{
		{Path: "Zebra", Title: "Zebra"},
		{Path: "quests/b", Title: "B"},
		{Path: "apple", Title: "Apple"},
		{Path: "quests/a", Title: "A"},
	}
	first, err := BuildTree(treeInput(entries...), "")
	if err != nil {
		t.Fatal(err)
	}
	// Reversed input must build the identical tree.
	rev := append([]VaultTreeEntry(nil), entries...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	second, err := BuildTree(treeInput(rev...), "")
	if err != nil {
		t.Fatal(err)
	}
	if RenderTree(first).HTML != RenderTree(second).HTML {
		t.Error("tree render differs by input order; order must be deterministic")
	}
	// Root pages sorted case-insensitively: apple before Zebra.
	if len(first.Pages) != 2 || first.Pages[0].Path != "apple" || first.Pages[1].Path != "Zebra" {
		t.Fatalf("root page order = %+v, want [apple Zebra]", first.Pages)
	}
	if len(first.Folders) != 1 || len(first.Folders[0].Pages) != 2 ||
		first.Folders[0].Pages[0].Path != "quests/a" || first.Folders[0].Pages[1].Path != "quests/b" {
		t.Fatalf("quests page order = %+v", first.Folders[0].Pages)
	}
}

func TestBuildTreeActiveMarking(t *testing.T) {
	in := treeInput(
		VaultTreeEntry{Path: "quests/ambush", Title: "Ambush"},
		VaultTreeEntry{Path: "welcome", Title: "Welcome"},
	)
	// Extensionless and .md active ids both match (case-insensitive).
	for _, active := range []string{"quests/ambush", "Quests/Ambush.md"} {
		root, err := BuildTree(in, active)
		if err != nil {
			t.Fatal(err)
		}
		frag := RenderTree(root)
		if !strings.Contains(frag.HTML, `href="/p/quests/ambush" aria-current="page"`) {
			t.Errorf("active %q: missing aria-current=page: %s", active, frag.HTML)
		}
		if strings.Count(frag.HTML, `aria-current="page"`) != 1 {
			t.Errorf("active %q: want exactly one aria-current, got: %s", active, frag.HTML)
		}
	}
}

func TestBuildTreeUnfilteredRefused(t *testing.T) {
	if _, err := BuildTree(VaultTreeInput{}, ""); err == nil {
		t.Error("expected UNFILTERED_INPUT rejection for Filtered=false")
	}
}

func TestRenderTreeStructure(t *testing.T) {
	root, err := BuildTree(treeInput(
		VaultTreeEntry{Path: "quests/ambush", Title: "Ambush", Secret: true},
		VaultTreeEntry{Path: "quests/deep/heist", Title: "Heist"},
		VaultTreeEntry{Path: "welcome", Title: "Welcome"},
	), "welcome")
	if err != nil {
		t.Fatal(err)
	}
	frag := RenderTree(root)
	if frag.Target != "vault-tree" {
		t.Errorf("target = %q, want vault-tree", frag.Target)
	}
	for _, want := range []string{
		`<nav aria-label="Vault tree"`,
		`data-plugin="vault-tree"`,
		`id="vault-tree"`,
		`data-sse-fragment="vault-tree"`,
		`aria-live="polite"`,
		`<details class="vt-details" open>`, // first level open
		`<summary class="vt-summary"`,
		`data-tree-path="quests"`,
		`data-tree-path="quests/deep"`,
		`data-tree-path="welcome"`,
		`<a class="vt-link" href="/p/welcome" aria-current="page">Welcome</a>`,
		`<span class="vt-badge">secret</span>`, // text badge, never color-only
	} {
		if !strings.Contains(frag.HTML, want) {
			t.Errorf("fragment missing %q:\n%s", want, frag.HTML)
		}
	}
	// Nested folders stay closed.
	if strings.Count(frag.HTML, `<details class="vt-details">`) != 1 {
		t.Errorf("want exactly one closed (nested) details:\n%s", frag.HTML)
	}
}

func TestRenderTreeEscapesAllStrings(t *testing.T) {
	// Secret titles never reach the tree (the caller drops them); anything
	// that does arrive is escaped.
	root, err := BuildTree(treeInput(
		VaultTreeEntry{Path: "quests/evil", Title: `<script>alert(1)</script>`},
		VaultTreeEntry{Path: `weird/"q"`, Title: "Q"},
	), "")
	if err != nil {
		t.Fatal(err)
	}
	got := RenderTree(root).HTML
	if strings.Contains(got, "<script>") {
		t.Errorf("unescaped HTML in fragment:\n%s", got)
	}
	if strings.Contains(got, `data-tree-path="weird/"q""`) {
		t.Errorf("unescaped attribute in fragment:\n%s", got)
	}
}

func TestVaultTreeCheckPluginPasses(t *testing.T) {
	p := NewVaultTreePlugin()
	if rep := CheckPlugin(p); !rep.OK() {
		t.Fatalf("CheckPlugin findings: %v", rep.Findings)
	}
	if got := len(p.CSS()); got > MaxPluginCSSBytes {
		t.Fatalf("CSS %d bytes exceeds %d cap", got, MaxPluginCSSBytes)
	}
	slots := p.Slots()
	if len(slots) != 1 {
		t.Fatalf("slots = %d, want 1", len(slots))
	}
	s := slots[0]
	if s.SlotName != "sidebar-left" || s.ID != VaultTreePluginID ||
		s.Component != VaultTreePluginID || s.Priority != 20 ||
		!s.SecretFiltered || s.PluginID != VaultTreePluginID {
		t.Errorf("slot = %+v, want sidebar-left/vault-tree prio 20 secret-filtered", s)
	}
	if p.Routes() != nil {
		t.Errorf("routes = %v, want nil", p.Routes())
	}
	if p.JS() != "" {
		t.Errorf("JS = %q, want empty", p.JS())
	}
	if err := p.ValidateConfig(nil); err != nil {
		t.Errorf("empty config rejected: %v", err)
	}
	if err := p.ValidateConfig(map[string]any{"nope": true}); err == nil {
		t.Error("unknown config key accepted")
	}
}
