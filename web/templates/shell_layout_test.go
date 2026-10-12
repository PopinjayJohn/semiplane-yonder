package templates

import (
	"strings"
	"testing"
)

// Layout contract: main content first in the DOM, then the two rails as
// siblings (the shell used to nest both slots in one <details> below main,
// which stacked them under the page). Each rail is a native, open <details>
// so it collapses without JS, and core renders its fallback content only when
// no plugin fills the slot (an enabled plugin replaces the fallback instead
// of duplicating it).
func TestShellRailsAndFallbacks(t *testing.T) {
	render := func(d PageData) string {
		var b strings.Builder
		if err := Shell(d, nil, PageBody(d)).Render(t.Context(), &b); err != nil {
			t.Fatalf("render: %v", err)
		}
		return b.String()
	}
	base := PageData{
		Title:     "T",
		Nav:       []NavEntry{{Path: "welcome", Title: "Welcome"}},
		Backlinks: []Backlink{{Path: "lore", Title: "Lore"}},
	}

	// No plugins: both rails exist, each an open details with its own
	// summary, and the core fallbacks render inside them.
	plain := render(base)
	for _, want := range []string{
		`<section class="rail rail-left" aria-label="Vault navigation">`,
		`<details class="drawer" open><summary>Vault navigation</summary>`,
		`<section class="rail rail-right" aria-label="On this page &amp; links">`,
		`<details class="drawer" open><summary>On this page &amp; links</summary>`,
		`<ul class="nav">`,
		`<ul class="backlinks">`,
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("unplugged shell missing %q", want)
		}
	}
	// Main precedes both rails (mobile-first DOM order).
	mainAt := strings.Index(plain, `<main class="sheet-col"`)
	leftAt := strings.Index(plain, `class="rail rail-left"`)
	rightAt := strings.Index(plain, `class="rail rail-right"`)
	if mainAt < 0 || leftAt < mainAt || rightAt < leftAt {
		t.Errorf("DOM order wrong: main=%d left=%d right=%d", mainAt, leftAt, rightAt)
	}
	// The old single-drawer shape is gone.
	if strings.Contains(plain, `<summary>On this page & links</summary>`) {
		t.Error("shell still nests both sidebars in one drawer")
	}

	// Plugins fill the sidebars and page-actions: their fragments render and
	// the core fallbacks step aside (no duplicate Search/Edit, no duplicate
	// nav list).
	filled := base
	filled.Slots = []SlotMark{
		{SlotName: "sidebar-left", ID: "vault-tree", Component: "vault-tree", PluginID: "vault-tree", HTML: "<nav>tree</nav>"},
		{SlotName: "sidebar-right", ID: "page-companions", Component: "page-companions", PluginID: "page-companions", HTML: "<section>companions</section>"},
		{SlotName: "page-actions", ID: "quick-actions", Component: "quick-actions", PluginID: "quick-actions", HTML: "<section>actions</section>"},
	}
	got := render(filled)
	for _, want := range []string{
		`<div data-plugin-slot="sidebar-left" data-component="vault-tree" data-plugin="vault-tree"><nav>tree</nav></div>`,
		`<div data-plugin-slot="sidebar-right" data-component="page-companions" data-plugin="page-companions"><section>companions</section></div>`,
		`<div data-plugin-slot="page-actions" data-component="quick-actions" data-plugin="quick-actions"><section>actions</section></div>`,
		// The live-updates status stays even when the plugin owns the slot.
		`data-sse-state`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("filled shell missing %q", want)
		}
	}
	for _, unwanted := range []string{`<ul class="nav">`, `<ul class="backlinks">`, `href="/search"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("filled shell kept the core fallback %q (duplicate of the plugin)", unwanted)
		}
	}
}

// The footer slot hosts plugin fragments INSIDE the shell's
// footer.colophon — the page's only contentinfo landmark. A fragment that
// renders its own <footer> nests a second contentinfo and fails axe
// (landmark-no-duplicate-contentinfo / -is-top-level, hit by the
// vault-colophon fragment in CI): whatever mounts there, the document
// keeps exactly one footer element.
func TestShellFooterSlotKeepsOneContentinfo(t *testing.T) {
	d := PageData{
		Title: "T",
		Slots: []SlotMark{
			{SlotName: "footer", ID: "vault-colophon", Component: "vault-colophon", PluginID: "vault-colophon",
				HTML: `<div data-plugin="vault-colophon"><section aria-label="About this vault"><p>Acme</p></section></div>`},
		},
	}
	var b strings.Builder
	if err := Shell(d, nil, PageBody(d)).Render(t.Context(), &b); err != nil {
		t.Fatalf("render: %v", err)
	}
	got := b.String()
	if n := strings.Count(got, "<footer"); n != 1 {
		t.Errorf("shell renders %d <footer> elements, want exactly 1 (the shell's own)", n)
	}
	if !strings.Contains(got, `<div data-plugin-slot="footer" data-component="vault-colophon" data-plugin="vault-colophon">`) {
		t.Error("footer slot fragment not mounted inside the shell footer")
	}
}
